package owner

import (
	"net/http"
)

// Permission enumerates the actions that the local dashboard routes protect.
// Each permission is granted to a fixed set of roles so the server — not the
// UI — decides whether a session can call a given endpoint. Adding a new
// permission requires adding it here, mapping the roles it is granted to in
// the switch in HasPermission, and gating the relevant route with
// RequirePermission. Removing a permission means an audit of every handler
// that referenced it.
type Permission string

const (
	// CanEditBusiness covers PUT /api/v1/owner/business: the business profile
	// is the single source of truth for the merchant's contact details,
	// currency and locale, and changing it has pricing and reconciliation
	// consequences. Only the owner may edit it.
	CanEditBusiness Permission = "business.edit"
	// CanViewPricing covers GET /api/v1/owner/pricing: every signed-in role
	// needs to read the price book to do their job, but only the owner may
	// change prices.
	CanViewPricing Permission = "pricing.view"
	// CanEditPricing covers PUT /api/v1/owner/pricing. Operators may read
	// pricing to quote customers and answer questions, but changing prices is
	// the owner's prerogative.
	CanEditPricing Permission = "pricing.edit"
	// CanManageOperators covers the operator CRUD endpoints under
	// /api/v1/owner/operators. Membership of the local team is the owner's
	// responsibility — an operator cannot create or disable peer accounts,
	// because doing so would let a compromised operator lock the owner out.
	CanManageOperators Permission = "operators.manage"
	// CanViewOrders covers GET /api/v1/owner/orders(/:id). Every signed-in role
	// needs to see incoming orders to do their job; changing status is separate.
	CanViewOrders Permission = "orders.view"
	// CanEditOrders covers status-changing operations (approve, cancel, dispatch)
	// on existing orders. Only the owner may modify orders.
	CanEditOrders Permission = "orders.edit"
)

// HasPermission reports whether the supplied role has been granted the
// permission. The owner holds every permission; the operator holds only the
// read-side of pricing. Future role additions (e.g. a read-only auditor) go
// here and only here.
func HasPermission(role Role, permission Permission) bool {
	if !role.Valid() {
		return false
	}
	switch permission {
	case CanEditBusiness:
		return role == RoleOwner
	case CanViewPricing:
		return role == RoleOwner || role == RoleOperator
	case CanEditPricing:
		return role == RoleOwner
	case CanManageOperators:
		return role == RoleOwner
	case CanViewOrders:
		// Both owner and operator need to see the order queue to do their job.
		return role == RoleOwner || role == RoleOperator
	case CanEditOrders:
		return role == RoleOwner
	default:
		// An unknown permission is a programming error: deny by default so
		// the call site cannot silently grant access to a typo'd permission.
		return false
	}
}

// RequirePermission is the HTTP-side enforcement of HasPermission. The caller
// has already verified the request via ownerSession (which has confirmed the
// loopback origin, the CSRF token and the session cookie) and has the
// resolved Subject in hand. This helper only consults the role and writes a
// 403 response when the permission is missing, returning a boolean so the
// handler can short-circuit on denial.
func RequirePermission(w http.ResponseWriter, subject Subject, permission Permission) bool {
	if HasPermission(subject.Role, permission) {
		return true
	}
	http.Error(w, "role is not permitted to perform this action", http.StatusForbidden)
	return false
}