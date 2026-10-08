package fixture

import "testing"

func TestActiveEntriesHaveValuesRequiredByTheMinifluxAPI(t *testing.T) {
	for _, entry := range Expected.ActiveEntries {
		if entry.Author == nil || entry.Content == nil || entry.Starred == nil || entry.Tags == nil || entry.Comments == nil {
			t.Errorf("active entry %d contains SQL NULL in an API-projected field", entry.ID)
		}
	}
}

func TestRemovedEntriesCarryTheNullableEdgeCases(t *testing.T) {
	var eligible, emptyHash *Entry
	for _, entry := range Expected.RemovedEntries {
		switch entry.Hash {
		case EligibleRemovedHash:
			eligible = &entry
		case "":
			emptyHash = &entry
		}
	}
	if eligible == nil {
		t.Fatal("eligible removed entry is missing")
	}
	if eligible.Author != nil || eligible.Content != nil || eligible.Starred != nil || eligible.Tags != nil || eligible.Comments != nil {
		t.Fatal("eligible removed entry must exercise SQL NULL for each nullable field")
	}
	if emptyHash == nil || emptyHash.Author == nil || emptyHash.Content == nil || emptyHash.Starred == nil || emptyHash.Tags == nil || emptyHash.Comments == nil {
		t.Fatal("empty-hash removed entry must use non-NULL empty values")
	}
}
