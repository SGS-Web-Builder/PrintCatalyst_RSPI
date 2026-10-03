//go:build !production

package licensing

// ControlPlanePrivateKeyHex is the private half of the control-plane
// key pair. The local stub uses it to sign the test entitlements it
// returns; production builds MUST NOT bundle this constant — it is
// only here so the on-device test fixtures exercise the same wire
// format the production control plane will sign.
const ControlPlanePrivateKeyHex = "ead9d049c8b9368eb9ddc57c4948823896185e061710fe3008a6e88e2613c282fe14f6211bb162b0d55afdf9f9dfbe223a88114c3b1823386355ec2fe1eccfa3"
