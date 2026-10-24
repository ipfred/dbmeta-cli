package guard

import (
	"errors"
	"strings"
	"testing"
)

func TestIdent(t *testing.T) {
	good := []string{"t_order", "order-history", "a$b", "订单表", "T1", strings.Repeat("a", 64)}
	for _, s := range good {
		if err := Ident("表", s); err != nil {
			t.Errorf("%q should be valid: %v", s, err)
		}
	}
	bad := []string{
		"", " ", "a b", "%", "t%", "a*", "a?", "`a`", "a`b", "a'b", `a"b`, "a.b", "..", ".", "a/b", `a\b`,
		"../etc", "a;drop", "a\nb", "a\x00b", "a,b", "(a)", strings.Repeat("a", 65), "a=b", "a#b", "a--b ",
	}
	for _, s := range bad {
		err := Ident("表", s)
		if err == nil {
			t.Errorf("%q should be rejected", s)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: want ErrInvalid, got %v", s, err)
		}
	}
}

func TestInstanceName(t *testing.T) {
	for _, s := range []string{"order_prod", "a-1", "A"} {
		if err := InstanceName(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "订单", "a b", "a.b", "a/b", strings.Repeat("x", 65)} {
		if err := InstanceName(s); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestDatabase(t *testing.T) {
	if err := Database(nil, "any"); err != nil {
		t.Error(err)
	}
	if err := Database([]string{"a", "b"}, "b"); err != nil {
		t.Error(err)
	}
	if err := Database([]string{"a", "b"}, "c"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("want ErrNotAllowed, got %v", err)
	}
}
