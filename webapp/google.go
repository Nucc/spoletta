package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Google connector reads and writes Google Sheets spreadsheets and
// Google Docs documents, and finds them in Drive, as a service account:
// the token an admin sets is the account's JSON key, with which the
// connector signs in to Google for an hour at a time. A service account
// sees only the files shared with its email address, as anyone would, so
// sharing a spreadsheet with it, as an editor or a viewer, is what lets
// flows at it.
//
// Sheets, Docs and Drive each have a host of their own; the server's URL
// stays www.googleapis.com, or names one host for all of them, such as a
// proxy's.

var googleConnector = &connector{
	Name:     "google",
	Title:    "Google Sheets and Docs",
	Category: "Docs",
	Description: "Google Sheets and Google Docs: find files shared with Spoletta, read a spreadsheet's rows and " +
		"append or change them, read a document and add to it.",
	URL: googleAPIs,
	TokenHelp: "A service account's JSON key, from the Google Cloud console, in a project with the Sheets, Docs and " +
		"Drive APIs on. Share each spreadsheet and document flows use with the account's email address.",
	endpoint:   googleEndpoint,
	authorize:  googleAuthorize,
	checkToken: func(token string) error { _, err := parseServiceAccount(token); return err },
	Operations: []operation{
		{Name: "find_files", Title: "Find files", Description: "Finds the spreadsheets and documents shared with the service account, by name, the latest changed first.",
			Params: []param{{Name: "name", Type: paramString, Description: "Only files whose name has this in it"},
				{Name: "type", Type: paramString, Enum: []string{"spreadsheet", "document"}, Description: "spreadsheet or document; both when left out"},
				{Name: "limit", Type: paramInteger, Description: fmt.Sprintf("How many to list, at most %d; 20 when left out", maxItems)}},
			run: gFindFiles},
		{Name: "get_spreadsheet", Title: "Get a spreadsheet", Description: "Gets a spreadsheet's title and its sheets, with how many rows and columns each has.",
			Params: []param{gSpreadsheet}, run: gGetSpreadsheet},
		{Name: "read_rows", Title: "Read rows", Description: "Reads a range of a spreadsheet: by default each row as an object named by the first row's headings.",
			Params: []param{gSpreadsheet, gRange,
				{Name: "headers", Type: paramBoolean, Description: "Whether the range's first row names its columns, so each row after it is an object; true when left out"}},
			run: gReadRows},
		{Name: "read_document", Title: "Read a document", Description: "Gets a Google Docs document's title and text.",
			Params: []param{gDocument}, run: gReadDocument},

		{Name: "append_rows", Title: "Append rows", Description: "Adds rows after the last one of a range's table.", Write: true,
			Params: []param{gSpreadsheet, gRange, gRows}, run: gAppendRows},
		{Name: "update_cells", Title: "Update cells", Description: "Writes values into a range, in place of what it holds.", Write: true,
			Params: []param{gSpreadsheet, gRange, gRows}, run: gUpdateCells},
		{Name: "append_to_document", Title: "Add to a document", Description: "Adds text at the end of a Google Docs document.", Write: true,
			Params: []param{gDocument, {Name: "text", Type: paramString, Required: true, Description: "The text to add, which starts on a line of its own"}},
			run:    gAppendToDocument},
	},
}

var (
	gFileID      = regexp.MustCompile(`^[A-Za-z0-9_-]{10,200}$`)
	gSpreadsheet = param{Name: "spreadsheet_id", Type: paramString, Required: true, Pattern: gFileID,
		Description: "The spreadsheet's id, from its URL: docs.google.com/spreadsheets/d/<id>/edit"}
	gDocument = param{Name: "document_id", Type: paramString, Required: true, Pattern: gFileID,
		Description: "The document's id, from its URL: docs.google.com/document/d/<id>/edit"}
	gRange = param{Name: "range", Type: paramString, Required: true, Pattern: regexp.MustCompile(`^[^/\\?#\x00-\x1f]{1,200}$`),
		Description: "The range, in A1 notation, such as Sheet1!A1:D or Sheet1"}
	gRows = param{Name: "rows", Type: paramString, Required: true,
		Description: `The rows, as JSON: a list of lists of values, such as [["a", 1]], or objects, or one object, whose fields name the range's columns by its first row's headings`}
)

const googleAPIs = "https://www.googleapis.com"

// The hosts of Google's APIs, by the first part of a connector's path.
var googleHosts = map[string]string{"sheets": "https://sheets.googleapis.com", "docs": "https://docs.googleapis.com"}

func googleEndpoint(base, path string) string {
	if strings.TrimSuffix(base, "/") == googleAPIs {
		api, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if host, ok := googleHosts[api]; ok {
			return host + "/" + rest
		}
	}
	return strings.TrimSuffix(base, "/") + path
}

// A service account's JSON key, the part of it the connector uses.
type serviceAccount struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	key          *rsa.PrivateKey
}

func parseServiceAccount(token string) (*serviceAccount, error) {
	var sa serviceAccount
	if err := json.Unmarshal([]byte(token), &sa); err != nil || sa.Type != "service_account" || sa.ClientEmail == "" {
		return nil, errors.New("the token must be a service account's JSON key, the whole file the Google Cloud console downloads")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("the service account's key has no private_key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	rsaKey, ok := key.(*rsa.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("the service account's private_key isn't an RSA key")
	}
	sa.key = rsaKey
	return &sa, nil
}

// Where Google gives a service account an access token; tests change it.
var googleTokenURL = "https://oauth2.googleapis.com/token"

const googleScopes = "https://www.googleapis.com/auth/spreadsheets https://www.googleapis.com/auth/documents https://www.googleapis.com/auth/drive.readonly"

// The access tokens service accounts got, by their key's hash, until a
// minute before they expire.
var googleTokens = struct {
	sync.Mutex
	byKey map[[32]byte]googleToken
}{byKey: map[[32]byte]googleToken{}}

type googleToken struct {
	access  string
	expires time.Time
}

func googleAuthorize(ctx context.Context, req *http.Request, token string) error {
	access, err := googleAccess(ctx, token)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	return nil
}

func googleAccess(ctx context.Context, token string) (string, error) {
	hash := sha256.Sum256([]byte(token))
	googleTokens.Lock()
	t, ok := googleTokens.byKey[hash]
	googleTokens.Unlock()
	if ok && time.Now().Before(t.expires) {
		return t.access, nil
	}
	sa, err := parseServiceAccount(token)
	if err != nil {
		return "", err
	}
	now := time.Now()
	assertion, err := sa.assertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", fmt.Errorf("signing in to Google: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		msg := out.Description
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("Google turned the service account %s away: %s", sa.ClientEmail, msg)
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	googleTokens.Lock()
	googleTokens.byKey[hash] = googleToken{access: out.AccessToken, expires: now.Add(time.Duration(out.ExpiresIn)*time.Second - time.Minute)}
	googleTokens.Unlock()
	return out.AccessToken, nil
}

// The JWT the service account signs to get an access token.
func (sa *serviceAccount) assertion(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": sa.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{"iss": sa.ClientEmail, "scope": googleScopes, "aud": googleTokenURL,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	signed := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(nil, sa.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

var gMimeTypes = map[string]string{"spreadsheet": "application/vnd.google-apps.spreadsheet", "document": "application/vnd.google-apps.document"}

func gFindFiles(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	limit := int64(20)
	if a.has("limit") {
		limit = a.int("limit")
	}
	if limit < 1 || limit > maxItems {
		return nil, fmt.Errorf("limit must be from 1 to %d", maxItems)
	}
	terms := []string{"trashed = false"}
	if name := a.str("name"); name != "" {
		terms = append(terms, "name contains '"+strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(name)+"'")
	}
	if t := a.str("type"); t != "" {
		terms = append(terms, "mimeType = '"+gMimeTypes[t]+"'")
	} else {
		terms = append(terms, "(mimeType = '"+gMimeTypes["spreadsheet"]+"' or mimeType = '"+gMimeTypes["document"]+"')")
	}
	q := url.Values{"q": {strings.Join(terms, " and ")}, "pageSize": {strconv.FormatInt(limit, 10)}, "orderBy": {"modifiedTime desc"},
		"fields": {"files(id,name,mimeType,modifiedTime,webViewLink)"}, "supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}}
	var out struct {
		Files []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			MimeType     string `json:"mimeType"`
			ModifiedTime string `json:"modifiedTime"`
			WebViewLink  string `json:"webViewLink"`
		} `json:"files"`
	}
	if err := c.do(ctx, http.MethodGet, "/drive/v3/files", q, nil, &out); err != nil {
		return nil, err
	}
	files := []map[string]any{}
	for _, f := range out.Files {
		kind := "document"
		if f.MimeType == gMimeTypes["spreadsheet"] {
			kind = "spreadsheet"
		}
		files = append(files, map[string]any{"key": f.ID, "id": f.ID, "name": f.Name, "type": kind, "modifiedAt": f.ModifiedTime, "url": f.WebViewLink})
	}
	return files, nil
}

func gSheetPath(a opArgs) string {
	return "/sheets/v4/spreadsheets/" + url.PathEscape(a.str("spreadsheet_id"))
}

func gGetSpreadsheet(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	var out struct {
		Properties struct {
			Title string `json:"title"`
		} `json:"properties"`
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
				Grid  struct {
					Rows    int `json:"rowCount"`
					Columns int `json:"columnCount"`
				} `json:"gridProperties"`
			} `json:"properties"`
		} `json:"sheets"`
		URL string `json:"spreadsheetUrl"`
	}
	q := url.Values{"fields": {"properties.title,spreadsheetUrl,sheets.properties(title,gridProperties(rowCount,columnCount))"}}
	if err := c.do(ctx, http.MethodGet, gSheetPath(a), q, nil, &out); err != nil {
		return nil, err
	}
	sheets := []map[string]any{}
	for _, s := range out.Sheets {
		sheets = append(sheets, map[string]any{"title": s.Properties.Title, "rows": s.Properties.Grid.Rows, "columns": s.Properties.Grid.Columns})
	}
	return map[string]any{"id": a.str("spreadsheet_id"), "title": out.Properties.Title, "url": out.URL, "sheets": sheets}, nil
}

// The values of a range, each cell as it shows.
func gValues(ctx context.Context, c connectorClient, a opArgs, rng string) ([][]any, error) {
	var out struct {
		Values [][]any `json:"values"`
	}
	if err := c.do(ctx, http.MethodGet, gSheetPath(a)+"/values/"+url.PathEscape(rng), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Values, nil
}

func gReadRows(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	values, err := gValues(ctx, c, a, a.str("range"))
	if err != nil {
		return nil, err
	}
	if a.has("headers") && !a.bool("headers") {
		return map[string]any{"range": a.str("range"), "values": values}, nil
	}
	rows := []map[string]any{}
	if len(values) == 0 {
		return rows, nil
	}
	headings := make([]string, len(values[0]))
	for i, h := range values[0] {
		headings[i] = strings.TrimSpace(fmt.Sprint(h))
	}
	for _, v := range values[1:] {
		row := map[string]any{}
		for i, h := range headings {
			if h == "" {
				continue
			}
			row[h] = ""
			if i < len(v) {
				row[h] = v[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// The rows to write as lists of values: lists as they are, objects by the
// range's headings, the first row of its sheet.
func gRowValues(ctx context.Context, c connectorClient, a opArgs) ([][]any, error) {
	raw := strings.TrimSpace(a.str("rows"))
	if strings.HasPrefix(raw, "{") {
		raw = "[" + raw + "]"
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil || len(items) == 0 {
		return nil, errors.New(`rows must be JSON: a list of rows, each a list of values or an object`)
	}
	var headings []string
	out := [][]any{}
	for _, item := range items {
		var list []any
		if json.Unmarshal(item, &list) == nil {
			out = append(out, list)
			continue
		}
		var obj map[string]any
		if json.Unmarshal(item, &obj) != nil {
			return nil, errors.New("each row must be a list of values or an object")
		}
		if headings == nil {
			sheet, _, _ := strings.Cut(a.str("range"), "!")
			first, err := gValues(ctx, c, a, sheet+"!1:1")
			if err != nil {
				return nil, err
			}
			if len(first) == 0 {
				return nil, errors.New("an object's fields name columns by the first row's headings, and the sheet's first row is empty")
			}
			for _, h := range first[0] {
				headings = append(headings, strings.TrimSpace(fmt.Sprint(h)))
			}
		}
		row := make([]any, len(headings))
		known := 0
		for i, h := range headings {
			row[i] = ""
			if v, ok := obj[h]; ok {
				row[i] = gCell(v)
				known++
			}
		}
		if known == 0 && len(obj) > 0 {
			return nil, fmt.Errorf("none of the row's fields is a heading of the sheet, which are %s", strings.Join(headings, ", "))
		}
		out = append(out, row)
	}
	return out, nil
}

// A value as a cell takes it: a number, a boolean or text, anything else
// as JSON.
func gCell(v any) any {
	switch v.(type) {
	case string, float64, bool, nil:
		return v
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// What Sheets says it wrote.
type gUpdate struct {
	UpdatedRange string `json:"updatedRange"`
	UpdatedRows  int    `json:"updatedRows"`
	UpdatedCells int    `json:"updatedCells"`
}

func gWritten(out gUpdate) map[string]any {
	return map[string]any{"range": out.UpdatedRange, "rows": out.UpdatedRows, "cells": out.UpdatedCells}
}

func gAppendRows(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	rows, err := gRowValues(ctx, c, a)
	if err != nil {
		return nil, err
	}
	var out struct {
		Updates gUpdate `json:"updates"`
	}
	q := url.Values{"valueInputOption": {"USER_ENTERED"}, "insertDataOption": {"INSERT_ROWS"}}
	if err := c.do(ctx, http.MethodPost, gSheetPath(a)+"/values/"+url.PathEscape(a.str("range"))+":append", q, map[string]any{"values": rows}, &out); err != nil {
		return nil, err
	}
	return gWritten(out.Updates), nil
}

func gUpdateCells(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	rows, err := gRowValues(ctx, c, a)
	if err != nil {
		return nil, err
	}
	var out gUpdate
	q := url.Values{"valueInputOption": {"USER_ENTERED"}}
	if err := c.do(ctx, http.MethodPut, gSheetPath(a)+"/values/"+url.PathEscape(a.str("range")), q, map[string]any{"values": rows}, &out); err != nil {
		return nil, err
	}
	return gWritten(out), nil
}

func gDocPath(a opArgs) string {
	return "/docs/v1/documents/" + url.PathEscape(a.str("document_id"))
}

func gReadDocument(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	var out struct {
		Title string `json:"title"`
		Body  struct {
			Content []struct {
				Paragraph *struct {
					Elements []struct {
						TextRun *struct {
							Content string `json:"content"`
						} `json:"textRun"`
					} `json:"elements"`
				} `json:"paragraph"`
			} `json:"content"`
		} `json:"body"`
	}
	if err := c.do(ctx, http.MethodGet, gDocPath(a), nil, nil, &out); err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, block := range out.Body.Content {
		if block.Paragraph == nil {
			continue
		}
		for _, e := range block.Paragraph.Elements {
			if e.TextRun != nil {
				text.WriteString(e.TextRun.Content)
			}
		}
	}
	return map[string]any{"id": a.str("document_id"), "title": out.Title, "text": text.String(),
		"url": "https://docs.google.com/document/d/" + a.str("document_id") + "/edit"}, nil
}

func gAppendToDocument(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	text := a.str("text")
	if !strings.HasPrefix(text, "\n") {
		text = "\n" + text
	}
	body := map[string]any{"requests": []any{map[string]any{"insertText": map[string]any{"endOfSegmentLocation": map[string]any{}, "text": text}}}}
	if err := c.do(ctx, http.MethodPost, gDocPath(a)+":batchUpdate", nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"id": a.str("document_id"), "added": len(text)}, nil
}
