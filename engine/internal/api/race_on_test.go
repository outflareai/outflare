//go:build race

package api_test

// raceDetector says the tests were built with -race, which makes everything
// several times slower: a test that times something widens its limit.
const raceDetector = true
