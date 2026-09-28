package model

import "testing"

func TestParseRole(t *testing.T) {
	cases := map[string]Role{
		"DEVELOPER":       RoleDeveloper,
		"developer":       RoleDeveloper,
		"product manager": RoleProductManager,
		"PRODUCT_MANAGER": RoleProductManager,
		"product-manager": RoleProductManager,
		"  qa  ":          RoleQA,
		"Researcher":      RoleResearcher,
		"critic":          RoleCritic,
	}
	for in, want := range cases {
		got, ok := ParseRole(in)
		if !ok {
			t.Errorf("ParseRole(%q) not found", in)
			continue
		}
		if got != want {
			t.Errorf("ParseRole(%q) = %q, want %q", in, got, want)
		}
	}

	// Separators are dropped wherever they appear, so a stray trailing "_" is
	// tolerated rather than rejected.
	if got, ok := ParseRole("product_manager_"); !ok || got != RoleProductManager {
		t.Errorf(`ParseRole("product_manager_") = %q/%v, want PRODUCT_MANAGER`, got, ok)
	}

	for _, bad := range []string{"", "wizard", "DEVELOPERS", "qa review"} {
		if got, ok := ParseRole(bad); ok {
			t.Errorf("ParseRole(%q) = %q, want not found", bad, got)
		}
	}
}

func TestParseRoleCoversEveryRole(t *testing.T) {
	for _, r := range AllRoles {
		got, ok := ParseRole(string(r))
		if !ok {
			t.Errorf("ParseRole(%q) not found", r)
			continue
		}
		if got != r {
			t.Errorf("ParseRole(%q) = %q", r, got)
		}
	}
}

func TestRoleValid(t *testing.T) {
	if !RoleQA.Valid() {
		t.Error("RoleQA should be valid")
	}
	if Role("NOPE").Valid() {
		t.Error("an unknown role must not be valid")
	}
}
