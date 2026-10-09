package main

import "testing"

func TestEvacuationChecker(t *testing.T) {
	input := "3\n5\n1\n2\n2\n4\n"
	for _, output := range []string{
		">S<v<\n>^>S<\n>^>^<\n",
		">S<v<\n>^>S<\n>^>^<",
	} {
		if !checkEvacuation(input, output) {
			t.Fatalf("valid plan rejected: %q", output)
		}
	}
	for _, output := range []string{
		">S<v<\n>^>S<\n>^>v<\n",
		">S<v<\n>^<S<\n>^>^<\n",
		">S<v<\n>^>S<\n>^>^<\nextra\n",
	} {
		if checkEvacuation(input, output) {
			t.Fatalf("invalid plan accepted: %q", output)
		}
	}
}

func TestGardenBedsChecker(t *testing.T) {
	input := "6\n5\n9\n12\n3\n7\n2\n"
	want := "2\n2 3 5 7\n"
	for _, output := range []string{"2\n2 3 5 7\n", "2\n7 5 3 2\n"} {
		if !checkGardenBeds(input, output, want) {
			t.Fatalf("valid answer rejected: %q", output)
		}
	}
	for _, output := range []string{"1\n2 3 5 7\n", "2\n2 3 5 8\n", "2\n2 9 5 7\n"} {
		if checkGardenBeds(input, output, want) {
			t.Fatalf("invalid answer accepted: %q", output)
		}
	}
}
