package workflow

import "testing"

func TestConcurrencyShapes(t *testing.T) {
	bare := mustParse(t, "name: w\non: [push]\nconcurrency: deploy\njobs:\n  a:\n    steps:\n      - run: x\n")
	c, err := bare.Concurrency()
	if err != nil || c == nil || c.Group != "deploy" || c.CancelInProgress {
		t.Fatalf("bare string: %+v, %v", c, err)
	}

	full := mustParse(t, "name: w\non: [push]\nconcurrency:\n  group: ${{ github.ref }}\n  cancel-in-progress: true\njobs:\n  a:\n    steps:\n      - run: x\n")
	c, err = full.Concurrency()
	if err != nil || c == nil || c.Group != "${{ github.ref }}" || !c.CancelInProgress {
		t.Fatalf("mapping: %+v, %v", c, err)
	}

	none := mustParse(t, "name: w\non: [push]\njobs:\n  a:\n    steps:\n      - run: x\n")
	if c, err := none.Concurrency(); err != nil || c != nil {
		t.Fatalf("absent: %+v, %v", c, err)
	}
}

// Guessing false here would silently queue what the author wanted cancelled.
func TestConcurrencyRefusesAnExpressionForCancelInProgress(t *testing.T) {
	wf := mustParse(t, "name: w\non: [push]\nconcurrency:\n  group: g\n  cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}\njobs:\n  a:\n    steps:\n      - run: x\n")
	if _, err := wf.Concurrency(); err == nil {
		t.Fatal("an expression for cancel-in-progress was silently accepted")
	}
}
