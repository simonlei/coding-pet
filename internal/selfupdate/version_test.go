package selfupdate

import "testing"

func TestIsNewer_HappyPath(t *testing.T) {
	cases := []struct {
		remote, local string
	}{
		{"v1.2.3", "v1.2.2"},
		{"v1.3.0", "v1.2.9"},
		{"v2.0.0", "v1.9.9"},
		{"1.2.3", "1.2.2"}, // 无前导 v 也可
	}
	for _, c := range cases {
		newer, err := IsNewer(c.remote, c.local)
		if err != nil {
			t.Fatalf("IsNewer(%q,%q) unexpected error: %v", c.remote, c.local, err)
		}
		if !newer {
			t.Errorf("IsNewer(%q,%q) = false, want true", c.remote, c.local)
		}
	}
}

func TestIsNewer_EqualOrOlder(t *testing.T) {
	cases := []struct {
		remote, local string
	}{
		{"v1.2.3", "v1.2.3"}, // 相等不升级
		{"v1.2.2", "v1.2.3"}, // 更旧不升级（防降级）
		{"v1.0.0", "v2.0.0"},
		{"v1.2.9", "v1.3.0"},
	}
	for _, c := range cases {
		newer, err := IsNewer(c.remote, c.local)
		if err != nil {
			t.Fatalf("IsNewer(%q,%q) unexpected error: %v", c.remote, c.local, err)
		}
		if newer {
			t.Errorf("IsNewer(%q,%q) = true, want false", c.remote, c.local)
		}
	}
}

func TestIsNewer_LocalNonSemver(t *testing.T) {
	// 本地为非 semver（如 dev 构建）时，任意正式远端都应允许升级
	cases := []string{"dev", "dev-abc123", "", "unknown"}
	for _, local := range cases {
		newer, err := IsNewer("v1.2.3", local)
		if err != nil {
			t.Fatalf("IsNewer(v1.2.3,%q) unexpected error: %v", local, err)
		}
		if !newer {
			t.Errorf("IsNewer(v1.2.3,%q) = false, want true", local)
		}
	}
}

func TestIsNewer_RemotePrereleaseSkipped(t *testing.T) {
	// 远端带 prerelease 后缀应跳过（不升级到预发布）
	newer, err := IsNewer("v1.2.3-rc1", "v1.2.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newer {
		t.Error("prerelease remote should be skipped (false), got true")
	}
}

func TestIsNewer_RemoteInvalid(t *testing.T) {
	// 远端非法（非 prerelease，仅格式错误）应返回 error
	cases := []string{"v1.2", "v1.x.0", "", "abc"}
	for _, remote := range cases {
		_, err := IsNewer(remote, "v1.0.0")
		if err == nil {
			t.Errorf("IsNewer(%q,v1.0.0) expected error, got nil", remote)
		}
	}
}
