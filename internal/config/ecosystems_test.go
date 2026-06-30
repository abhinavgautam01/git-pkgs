package config

import "testing"

func TestEcosystemFilterAllowsAllWhenUnset(t *testing.T) {
	filter := NewEcosystemFilter(nil, nil)

	for _, ecosystem := range []string{"npm", "rubygems", "golang"} {
		if !filter.Allows(ecosystem) {
			t.Fatalf("expected unset filter to allow %s", ecosystem)
		}
	}
}

func TestEcosystemFilterAllowList(t *testing.T) {
	filter := NewEcosystemFilter([]string{"npm", "gem"}, nil)

	if !filter.Allows("npm") {
		t.Fatal("expected npm to be allowed")
	}
	if !filter.Allows("rubygems") {
		t.Fatal("expected gem alias to allow rubygems")
	}
	if filter.Allows("pypi") {
		t.Fatal("expected pypi to be filtered")
	}
}

func TestEcosystemFilterIgnoreListWins(t *testing.T) {
	filter := NewEcosystemFilter([]string{"npm", "rubygems"}, []string{"npm"})

	if filter.Allows("npm") {
		t.Fatal("expected ignored npm to be filtered")
	}
	if !filter.Allows("rubygems") {
		t.Fatal("expected rubygems to remain allowed")
	}
}

func TestSplitConfigValues(t *testing.T) {
	values := splitConfigValues("npm, rubygems\npypi\tgolang")

	want := []string{"npm", "rubygems", "pypi", "golang"}
	if len(values) != len(want) {
		t.Fatalf("got %d values, want %d: %#v", len(values), len(want), values)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("value %d = %q, want %q", i, values[i], want[i])
		}
	}
}
