package crawl

import "testing"

func TestFoldLocaleTemplate(t *testing.T) {
	folded, loc, ok := FoldLocaleTemplate("/de/products/*")
	if !ok || folded != "/{locale}/products/*" || loc != "de" {
		t.Fatalf("de: %q %q %v", folded, loc, ok)
	}
	folded, loc, ok = FoldLocaleTemplate("/zh-cn/blog/*")
	if !ok || folded != "/{locale}/blog/*" || loc != "zh-cn" {
		t.Fatalf("zh-cn: %q %q %v", folded, loc, ok)
	}
	if _, _, ok := FoldLocaleTemplate("/blog/*"); ok {
		t.Fatal("blog should not fold")
	}
	if _, _, ok := FoldLocaleTemplate("/go/docs"); ok {
		t.Fatal("go is not a locale")
	}
	if _, _, ok := FoldLocaleTemplate("/{locale}/products/*"); ok {
		t.Fatal("already folded")
	}
	if !IsFoldedLocaleTemplate("/{locale}/products/*") || IsFoldedLocaleTemplate("/de/products/*") {
		t.Fatal("IsFoldedLocaleTemplate")
	}
}
