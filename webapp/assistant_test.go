package main

import "testing"

func TestSplitSetup(t *testing.T) {
	for _, c := range []struct {
		reply, text, file string
		found             bool
	}{
		{"Just an answer.", "Just an answer.", "", false},
		{"Done.\n\n```json\n{\"genie\": 1}\n```\n", "Done.", "{\"genie\": 1}", true},
		{"Done.\n```yaml\ngenie: 1\n```\nSign in to jira.", "Done.\n\nSign in to jira.", "genie: 1", true},
		// The file is the last block; an example before it stays in the text.
		{"Like ```json\n{}\n``` but\n```json\n{\"genie\": 1}\n```", "Like ```json\n{}\n``` but", "{\"genie\": 1}", true},
		{"A script:\n```bash\necho hi\n```", "A script:\n```bash\necho hi\n```", "", false},
	} {
		text, file, found := splitSetup(c.reply)
		if text != c.text || file != c.file || found != c.found {
			t.Errorf("splitSetup(%q) = %q, %q, %v, want %q, %q, %v", c.reply, text, file, found, c.text, c.file, c.found)
		}
	}
}
