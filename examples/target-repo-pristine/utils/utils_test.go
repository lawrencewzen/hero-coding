package utils

import (
	"reflect"
	"testing"
)

func TestParseRange_InclusiveBothEnds(t *testing.T) {
	got, err := ParseRange("1-5")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 3, 4, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRange(\"1-5\") = %v, want %v", got, want)
	}
}

func TestParseRange_SingleValue(t *testing.T) {
	got, err := ParseRange("3-3")
	if err != nil {
		t.Fatal(err)
	}
	want := []int{3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseRange(\"3-3\") = %v, want %v", got, want)
	}
}
