package main

import "testing"

// pyzmq 27.3.0's and pyreqwest 0.15.1's file names (a selection), plus decoys.
var files = []string{
	"pyzmq-27.3.0-cp312-abi3-macosx_10_15_universal2.whl",
	"pyzmq-27.3.0-cp312-abi3-manylinux_2_26_aarch64.manylinux_2_28_aarch64.whl",
	"pyzmq-27.3.0-cp312-abi3-manylinux_2_26_x86_64.manylinux_2_28_x86_64.whl",
	"pyzmq-27.3.0-cp312-abi3-musllinux_1_2_aarch64.whl",
	"pyzmq-27.3.0-cp312-abi3-win_amd64.whl",
	"pyzmq-27.3.0-cp312-abi3-win_arm64.whl",
	"pyzmq-27.3.0-cp313-cp313-android_24_arm64_v8a.whl",
	"pyzmq-27.3.0-cp314-cp314t-macosx_10_15_universal2.whl",
	"pyzmq-27.3.0-cp314-cp314t-manylinux_2_26_aarch64.manylinux_2_28_aarch64.whl",
	"pyzmq-27.3.0-cp314-cp314t-win_arm64.whl",
	"pyzmq-27.3.0-pp310-pypy310_pp73-manylinux_2_26_aarch64.manylinux_2_28_aarch64.whl",
	"pyreqwest-0.15.1-cp313-cp313-macosx_10_12_x86_64.whl",
	"pyreqwest-0.15.1-cp313-cp313-macosx_11_0_arm64.whl",
	"pyreqwest-0.15.1-cp313-cp313-manylinux_2_17_aarch64.manylinux2014_aarch64.whl",
	"pyreqwest-0.15.1-cp313-cp313-musllinux_1_1_aarch64.whl",
	"pyreqwest-0.15.1-cp314-cp314t-manylinux_2_17_aarch64.manylinux2014_aarch64.whl",
	"pyreqwest-0.15.1-cp314-cp314-manylinux_2_17_aarch64.manylinux2014_aarch64.whl",
	"decoy-1.0-cp313-cp313-manylinux_2_34_aarch64.whl", // newer glibc than the floor
	"decoy-1.0-cp313-cp313-macosx_14_0_arm64.whl",      // newer macOS than the floor
	"decoy-1.0-cp312-abi3-manylinux_2_17_aarch64.whl",  // only abi3: not for 3.14t
	"pure-1.0-py3-none-any.whl",
	"both-1.0-cp313-cp313-manylinux_2_17_aarch64.whl",  // ABI-specific beats
	"both-1.0-cp312-abi3-manylinux_2_28_aarch64.whl",   // the stable ABI
	"glibc-1.0-cp313-cp313-manylinux_2_17_aarch64.whl", // the newest glibc that fits
	"glibc-1.0-cp313-cp313-manylinux_2_28_aarch64.whl", // wins
}

func pick(t *testing.T, tg target, prefix string) string {
	best, bestRank := "", 0
	for _, f := range files {
		if len(f) < len(prefix) || f[:len(prefix)] != prefix {
			continue
		}
		if r, ok := tg.rank(f); ok && (best == "" || r < bestRank) {
			best, bestRank = f, r
		}
	}
	return best
}

func TestChoice(t *testing.T) {
	for _, tc := range []struct{ python, variant, target, pkg, want string }{
		{"3.13", "", "linux/arm64", "pyzmq-", "pyzmq-27.3.0-cp312-abi3-manylinux_2_26_aarch64.manylinux_2_28_aarch64.whl"},
		{"3.14", "freethreaded", "linux/arm64", "pyzmq-", "pyzmq-27.3.0-cp314-cp314t-manylinux_2_26_aarch64.manylinux_2_28_aarch64.whl"},
		{"3.13", "", "linux/amd64", "pyzmq-", "pyzmq-27.3.0-cp312-abi3-manylinux_2_26_x86_64.manylinux_2_28_x86_64.whl"},
		{"3.13", "", "darwin/arm64", "pyzmq-", "pyzmq-27.3.0-cp312-abi3-macosx_10_15_universal2.whl"},
		{"3.14", "freethreaded", "darwin/amd64", "pyzmq-", "pyzmq-27.3.0-cp314-cp314t-macosx_10_15_universal2.whl"},
		{"3.13", "", "windows/arm64", "pyzmq-", "pyzmq-27.3.0-cp312-abi3-win_arm64.whl"},
		{"3.14", "freethreaded", "windows/arm64", "pyzmq-", "pyzmq-27.3.0-cp314-cp314t-win_arm64.whl"},
		{"3.14", "freethreaded", "windows/amd64", "pyzmq-", ""}, // no cp314t win_amd64 above; never abi3
		{"3.13", "", "linux/arm64", "pyreqwest-", "pyreqwest-0.15.1-cp313-cp313-manylinux_2_17_aarch64.manylinux2014_aarch64.whl"},
		{"3.14", "freethreaded", "linux/arm64", "pyreqwest-", "pyreqwest-0.15.1-cp314-cp314t-manylinux_2_17_aarch64.manylinux2014_aarch64.whl"},
		{"3.14", "", "linux/arm64", "pyreqwest-", "pyreqwest-0.15.1-cp314-cp314-manylinux_2_17_aarch64.manylinux2014_aarch64.whl"},
		{"3.13", "", "darwin/arm64", "pyreqwest-", "pyreqwest-0.15.1-cp313-cp313-macosx_11_0_arm64.whl"},
		{"3.13", "", "darwin/amd64", "pyreqwest-", "pyreqwest-0.15.1-cp313-cp313-macosx_10_12_x86_64.whl"},
		{"3.13", "", "linux/arm64", "decoy-", "decoy-1.0-cp312-abi3-manylinux_2_17_aarch64.whl"},
		{"3.14", "freethreaded", "linux/arm64", "decoy-", ""},
		{"3.13", "", "darwin/arm64", "decoy-", ""},
		{"3.14", "freethreaded", "windows/amd64", "pure-", "pure-1.0-py3-none-any.whl"},
		{"3.13", "", "linux/arm64", "both-", "both-1.0-cp313-cp313-manylinux_2_17_aarch64.whl"},
		{"3.13", "", "linux/arm64", "glibc-", "glibc-1.0-cp313-cp313-manylinux_2_28_aarch64.whl"},
	} {
		tg, err := newTarget(tc.python, tc.variant, tc.target, "2.28", "11.0")
		if err != nil {
			t.Fatal(err)
		}
		if got := pick(t, tg, tc.pkg); got != tc.want {
			t.Errorf("%s %s %s %s: %q, want %q", tc.python, tc.variant, tc.target, tc.pkg, got, tc.want)
		}
	}
}
