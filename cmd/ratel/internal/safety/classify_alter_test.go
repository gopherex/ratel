package safety_test

import "testing"

func TestClassifyAlterTable(t *testing.T) {
	t.Parallel()
	checkClassified(t, alterCases())
}

func TestClassifyDrop(t *testing.T) {
	t.Parallel()
	checkClassified(t, dropCases())
}
