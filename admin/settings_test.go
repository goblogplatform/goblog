package admin_test

import (
	"testing"

	"goblog/admin"
	"goblog/blog"
)

// seededSettings mirrors tools/migrate.go seedDefaultSettings: every key a
// fresh install has, with its input type.
var seededSettings = map[string]string{
	"site_title": "text", "site_subtitle": "text", "site_logo_letters": "text", "site_tags": "text",
	"landing_page_image": "file", "favicon": "file", "custom_header_code": "textarea", "custom_footer_code": "textarea",
	"theme": "text", "robots_tag": "text", "site_url": "text", "comments_require_login": "checkbox",
	"plugin_directory_url": "text", "theme_directory_url": "text",
}

func seededSettingMap() map[string]blog.Setting {
	m := make(map[string]blog.Setting, len(seededSettings))
	for k, typ := range seededSettings {
		m[k] = blog.Setting{Key: k, Type: typ, Value: "v-" + k}
	}
	return m
}

// TestGroupSettings_EverySeededKeyHasALabelledHome checks each seeded key is
// placed in a named group (not Advanced) with a human label, in the tab
// order the page shows.
func TestGroupSettings_EverySeededKeyHasALabelledHome(t *testing.T) {
	groups := admin.GroupSettings(seededSettingMap())
	var ids []string
	placed := map[string]string{}
	for _, g := range groups {
		ids = append(ids, g.ID)
		for _, f := range g.Fields {
			if f.Label == "" || f.Label == f.Key {
				t.Errorf("%s: %s has no human label (got %q)", g.ID, f.Key, f.Label)
			}
			if f.Value != "v-"+f.Key || f.Type != seededSettings[f.Key] {
				t.Errorf("%s: %s lost its stored row: %+v", g.ID, f.Key, f.Setting)
			}
			if prev, dup := placed[f.Key]; dup {
				t.Errorf("%s appears in both %s and %s", f.Key, prev, g.ID)
			}
			placed[f.Key] = g.ID
		}
	}
	for key := range seededSettings {
		if placed[key] == "" {
			t.Errorf("seeded key %s is not on the page", key)
		} else if placed[key] == "advanced" {
			t.Errorf("seeded key %s fell through to Advanced", key)
		}
	}
	want := []string{"site", "appearance", "comments", "directories"}
	if len(ids) != len(want) {
		t.Fatalf("groups = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("groups = %v, want %v", ids, want)
			break
		}
	}
}

// TestGroupSettings_UnknownKeyLandsInAdvanced checks a key the layout does
// not know is still rendered, under Advanced with its raw key as the
// label, so a setting added by a plugin or a later migration never vanishes.
func TestGroupSettings_UnknownKeyLandsInAdvanced(t *testing.T) {
	m := seededSettingMap()
	m["zz_new_setting"] = blog.Setting{Key: "zz_new_setting", Type: "text", Value: "x"}
	m["aa_other_setting"] = blog.Setting{Key: "aa_other_setting", Type: "checkbox", Value: "true"}
	groups := admin.GroupSettings(m)
	last := groups[len(groups)-1]
	if last.ID != "advanced" || last.Title != "Advanced" {
		t.Fatalf("last group = %+v, want Advanced", last)
	}
	if len(last.Fields) != 2 || last.Fields[0].Key != "aa_other_setting" || last.Fields[1].Key != "zz_new_setting" {
		t.Fatalf("advanced fields = %+v, want the two unknown keys sorted", last.Fields)
	}
	for _, f := range last.Fields {
		if f.Label != f.Key || f.Help != "" {
			t.Errorf("advanced field %s: label %q help %q, want raw key and no help", f.Key, f.Label, f.Help)
		}
	}
	if f := last.Fields[0]; f.Type != "checkbox" || f.Value != "true" {
		t.Errorf("advanced field kept wrong row: %+v", f)
	}
}

// TestGroupSettings_EmptyGroupsOmitted checks a group whose keys are not in
// the database (an older install, or a test seeding one row) draws no tab,
// and that no settings at all means no Advanced tab either.
func TestGroupSettings_EmptyGroupsOmitted(t *testing.T) {
	groups := admin.GroupSettings(map[string]blog.Setting{
		"comments_require_login": {Key: "comments_require_login", Type: "checkbox", Value: "false"},
	})
	if len(groups) != 1 || groups[0].ID != "comments" || len(groups[0].Fields) != 1 {
		t.Fatalf("groups = %+v, want only comments", groups)
	}
	if groups := admin.GroupSettings(nil); len(groups) != 0 {
		t.Fatalf("groups = %+v, want none", groups)
	}
}
