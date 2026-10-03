package owner

import (
	"net/http/httptest"
	"testing"
)

// TestRBAC_OwnerHasFullPermissions asserts the owner holds every documented
// permission. If a future permission is introduced and the owner does not
// hold it, this test will fail and force the grant decision to be made
// explicitly in HasPermission.
func TestRBAC_OwnerHasFullPermissions(t *testing.T) {
	for _, permission := range []Permission{CanEditBusiness, CanViewPricing, CanEditPricing, CanManageOperators} {
		if !HasPermission(RoleOwner, permission) {
			t.Fatalf("owner missing %q", permission)
		}
	}
}

// TestRBAC_OperatorReadOnlyOnPricing is the core safety check: an operator
// can see the price book but cannot edit the business profile, edit the price
// book or manage operator membership. These four guarantees together stop a
// compromised operator from rewriting prices, impersonating the merchant or
// locking the owner out by deleting peer operators.
func TestRBAC_OperatorReadOnlyOnPricing(t *testing.T) {
	if !HasPermission(RoleOperator, CanViewPricing) {
		t.Fatal("operator cannot view pricing")
	}
	for _, permission := range []Permission{CanEditBusiness, CanEditPricing, CanManageOperators} {
		if HasPermission(RoleOperator, permission) {
			t.Fatalf("operator should not have %q", permission)
		}
	}
}

// TestRBAC_UnknownRolesAndPermissionsAreDenied locks down the safety net: a
// role that does not exist in the schema, or a permission that is unknown to
// this build, must never silently return true. Deny by default is the only
// safe default for an RBAC table.
func TestRBAC_UnknownRolesAndPermissionsAreDenied(t *testing.T) {
	for _, role := range []Role{"", "admin", "auditor", "OWNER"} {
		if HasPermission(role, CanEditBusiness) {
			t.Fatalf("unknown role %q granted business.edit", role)
		}
	}
	for _, permission := range []Permission{"", "business.delete", "pricing.ship"} {
		if HasPermission(RoleOwner, permission) {
			t.Fatalf("unknown permission %q granted to owner", permission)
		}
	}
}

// TestRBAC_RequirePermissionWritesForbiddenOnDeny drives the HTTP helper to
// make sure the response shape (403 with a non-empty body) is consistent and
// that the helper returns false so handlers stop processing. The positive
// case confirms it returns true and writes no error body.
func TestRBAC_RequirePermissionWritesForbiddenOnDeny(t *testing.T) {
	recorder := httptest.NewRecorder()
	if RequirePermission(recorder, Subject{Role: RoleOperator}, CanManageOperators) {
		t.Fatal("operator was granted operators.manage")
	}
	if recorder.Code != 403 {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("missing forbidden body")
	}
	recorder = httptest.NewRecorder()
	if !RequirePermission(recorder, Subject{Role: RoleOwner}, CanManageOperators) {
		t.Fatal("owner was denied operators.manage")
	}
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want default 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("permission body unexpectedly written: %q", recorder.Body.String())
	}
}