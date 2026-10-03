package localserver

import "testing"

func TestCustomerDetailsFieldPolicy(t *testing.T) {
	p := customerDetailsPolicy{Enabled: true, Fields: map[string]string{"customerName": "hidden", "customerPhone": "optional", "customerEmail": "required", "customerNotes": "hidden"}}
	name, phone, email, notes := "private", "", " person@example.com ", "private"
	values := map[string]*string{"customerName": &name, "customerPhone": &phone, "customerEmail": &email, "customerNotes": &notes}
	if err := p.apply(values); err != nil {
		t.Fatal(err)
	}
	if name != "" || notes != "" || email != "person@example.com" {
		t.Fatal("field policy not applied")
	}
	email = "  "
	if err := p.apply(values); err == nil {
		t.Fatal("required whitespace accepted")
	}
}
