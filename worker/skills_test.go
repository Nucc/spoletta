package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Each skill is a directory of its name holding its SKILL.md, whose
// frontmatter a description with a colon and a line break can't break, and
// pi gets each with --skill.
func TestWriteSkills(t *testing.T) {
	dir := t.TempDir()
	args, err := writeSkills(dir, []skill{
		{Name: "release-notes", Description: "Writes release notes: use when\nasked for them.", Content: "# Release notes\n\nList what changed."},
		{Name: "triage", Description: "Sorts bugs.", Content: "Label each bug."},
	})
	if err != nil {
		t.Fatal(err)
	}
	notes, triage := filepath.Join(dir, "skills", "release-notes"), filepath.Join(dir, "skills", "triage")
	if !slices.Equal(args, []string{"--skill", notes, "--skill", triage}) {
		t.Errorf("args = %v", args)
	}
	b, err := os.ReadFile(filepath.Join(notes, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nname: \"release-notes\"\ndescription: \"Writes release notes: use when\\nasked for them.\"\n---\n\n# Release notes\n\nList what changed.\n"
	if string(b) != want {
		t.Errorf("SKILL.md =\n%s\nwant\n%s", b, want)
	}
}

// A name that isn't pi's, such as one climbing out of the directory, is refused.
func TestWriteSkillsRefusesBadNames(t *testing.T) {
	for _, name := range []string{"../out", "Upper", "a--b", ""} {
		if _, err := writeSkills(t.TempDir(), []skill{{Name: name, Description: "d", Content: "c"}}); err == nil {
			t.Errorf("%q was written", name)
		}
	}
}
