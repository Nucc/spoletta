package main

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A query may rank the records it selects by how like they are to what its
// process takes, its like (queries.go), such as {@Incoming.summary}
// {@Incoming.description}, so an agent deciding whether a ticket is new is
// handed the tickets most like it rather than the latest. How like is the
// words they have in common, rarer words counting more, as BM25 weighs
// them: a record's words are those its fields hold, at any depth, lists and
// parts included but not the fields' names, split at whatever isn't a
// letter or a digit, lower-cased, a word of one letter and the commonest
// English words left out. A record sharing no word with it isn't selected.
// Nothing is stemmed or translated, so a ticket saying "sign-in" isn't like
// one saying "login": the agent judges what it is handed, and a broader
// search or a higher limit hands it more.

// BM25's usual weights: how soon a word said again stops counting more,
// and how much a long record's words count less.
const (
	likeSaturation = 1.2
	likeLength     = 0.75
)

// Words too common to tell records apart.
var commonWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`an and are as at be been but by can could did do does for from had has have he her his how if in into is it its may me my no not of on or our she so than that the their them then there these they this to too us was we were what when where which who why will with would you your`) {
		commonWords[w] = true
	}
}

// The words of text that tell it apart, as the comment above says, in order.
func likeWords(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(w) > 1 && !commonWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// The text a value holds, its strings and numbers at any depth joined by
// spaces; plain text as it is.
func contentText(content string) string {
	v := contentValue(content)
	if v == nil {
		return content
	}
	var b strings.Builder
	valueText(&b, v)
	return b.String()
}

func valueText(b *strings.Builder, v any) {
	switch x := v.(type) {
	case string:
		b.WriteString(x)
		b.WriteByte(' ')
	case json.Number:
		b.WriteString(x.String())
		b.WriteByte(' ')
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
		b.WriteByte(' ')
	case []any:
		for _, e := range x {
			valueText(b, e)
		}
	case map[string]any:
		for _, e := range x {
			valueText(b, e)
		}
	}
}

// The texts most like like first, by index, those sharing no word with it
// left out; texts as like it as each other keep their order. None when like
// has no word to compare.
func likeOrder(texts []string, like string) []int {
	var query []string
	for _, w := range likeWords(like) {
		if !slices.Contains(query, w) {
			query = append(query, w)
		}
	}
	if len(query) == 0 || len(texts) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, w := range query {
		wanted[w] = true
	}
	// How often each text says each word of like, how many words it has,
	// and how many texts say each word.
	counts := make([]map[string]int, len(texts))
	lengths := make([]int, len(texts))
	saying := map[string]int{}
	total := 0
	for i, t := range texts {
		ws := likeWords(t)
		lengths[i] = len(ws)
		total += len(ws)
		c := map[string]int{}
		for _, w := range ws {
			if wanted[w] {
				if c[w] == 0 {
					saying[w]++
				}
				c[w]++
			}
		}
		counts[i] = c
	}
	n := float64(len(texts))
	average := math.Max(float64(total)/n, 1)
	scores := make([]float64, len(texts))
	var out []int
	for i, c := range counts {
		var s float64
		for _, w := range query {
			f := float64(c[w])
			if f == 0 {
				continue
			}
			rarity := math.Log(1 + (n-float64(saying[w])+0.5)/(float64(saying[w])+0.5))
			s += rarity * f * (likeSaturation + 1) / (f + likeSaturation*(1-likeLength+likeLength*float64(lengths[i])/average))
		}
		if s > 0 {
			scores[i] = s
			out = append(out, i)
		}
	}
	slices.SortStableFunc(out, func(x, y int) int { return cmp.Compare(scores[y], scores[x]) })
	return out
}
