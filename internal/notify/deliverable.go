package notify

import "strings"

// Reserved domains that can never accept mail.
//
// RFC 2606 and RFC 6761 set these aside precisely so that software has names
// it can use in tests and examples without ever reaching a real mailbox. Mail
// addressed to one of them cannot be delivered by anyone, ever.
var reservedTLDs = []string{".invalid", ".test", ".example", ".localhost"}

var reservedDomains = []string{"example.com", "example.net", "example.org"}

// IsDeliverable reports whether an address is worth handing to a mail
// provider.
//
// This is not validation: a syntactically perfect address may still bounce,
// and that is the provider's business, not ours. It answers a narrower
// question, which is whether we already know the address is undeliverable
// before we spend a send on it.
//
// The reason this exists is a real incident. The activity simulator gives
// every synthetic member an address ending in @simulated.invalid. The worker
// dutifully queued borrow confirmations for them and handed each one to
// Resend, which accepted the call and then recorded a delayed delivery it
// could never complete. Hundreds of undeliverable sends against one sending
// domain is how that domain's reputation is spent, and the domain in question
// also carries real mail.
//
// The check belongs here, on the address, rather than on a synthetic flag.
// A librarian who typos an address into a reserved domain deserves the same
// protection, and a rule about the address cannot be bypassed by creating an
// undeliverable account some other way.
func IsDeliverable(address string) bool {
	addr := strings.ToLower(strings.TrimSpace(address))

	at := strings.LastIndex(addr, "@")
	if at < 1 || at == len(addr)-1 {
		return false // no local part, or no domain
	}
	domain := addr[at+1:]

	for _, tld := range reservedTLDs {
		// Both "mail.example.test" and a bare "localhost", which has no dot
		// and so does not match the suffix form.
		if domain == strings.TrimPrefix(tld, ".") || strings.HasSuffix(domain, tld) {
			return false
		}
	}
	for _, d := range reservedDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return false
		}
	}
	return true
}
