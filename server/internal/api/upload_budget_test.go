package api

import "testing"

func TestUploadGateSharesStagingBudget(t *testing.T) {
	var gate uploadGate
	release, err := gate.acquire("first", uploadStagingBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.acquire("second", 1); err == nil {
		t.Fatal("second member acquired space beyond the shared budget")
	}
	release()
	release()

	large, err := gate.acquire("second", uploadStagingBudget+1)
	if err != nil {
		t.Fatalf("a single larger configured upload should fit: %v", err)
	}
	if _, err := gate.acquire("third", 1); err == nil {
		t.Fatal("staging was available during a larger upload")
	}
	large()
	release, err = gate.acquire("third", 1)
	if err != nil {
		t.Fatalf("staging space was not released: %v", err)
	}
	release()
}
