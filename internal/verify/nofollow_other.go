//go:build !unix

package verify

// noFollow is zero where the platform has no O_NOFOLLOW. The gate runs on
// Linux and macOS runners, where it is not.
const noFollow = 0
