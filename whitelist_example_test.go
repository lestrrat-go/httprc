package httprc_test

import (
	"fmt"
	"regexp"

	"github.com/lestrrat-go/httprc/v3"
)

// ExampleRegexpWhitelist demonstrates why RegexpWhitelist patterns should be
// anchored. RegexpWhitelist matches each URL with (*regexp.Regexp).MatchString,
// which succeeds when the pattern matches any substring of the URL. An unanchored
// pattern such as `http://example.com` therefore allows URLs whose host is not
// example.com at all, while an anchored pattern such as `^https://example\.com/`
// restricts matches to the intended origin.
func ExampleRegexpWhitelist() {
	urls := []string{
		"https://example.com/data",            // legitimate
		"https://example.com.attacker.com/x",  // host is actually attacker.com
		"https://attacker.com/?u=example.com", // pattern appears in the query
	}

	// BAD: unanchored, dots unescaped. Matches example.com anywhere in the URL.
	bad := httprc.NewRegexpWhitelist().Add(regexp.MustCompile(`example.com`))

	// GOOD: anchored at the start, dots escaped, host terminated with `/`.
	good := httprc.NewRegexpWhitelist().Add(regexp.MustCompile(`^https://example\.com/`))

	for _, u := range urls {
		fmt.Printf("%-37s bad=%-5t good=%t\n", u, bad.IsAllowed(u), good.IsAllowed(u))
	}

	// OUTPUT:
	// https://example.com/data              bad=true  good=true
	// https://example.com.attacker.com/x    bad=true  good=false
	// https://attacker.com/?u=example.com   bad=true  good=false
}
