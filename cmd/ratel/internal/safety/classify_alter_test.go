package safety_test

import "testing"

func TestClassifyAlterTable(t *testing.T) {
	parser := newParser(t)

	t.Parallel()
	checkClassified(t, parser, alterCases())
}

func TestClassifyDrop(t *testing.T) {
	parser := newParser(t)

	t.Parallel()
	checkClassified(t, parser, dropCases())
}
