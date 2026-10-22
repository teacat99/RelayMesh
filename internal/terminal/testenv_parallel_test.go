package terminal

import "testing"

// 并行登记的自测（#200）。这里的测试都不建环境、不并行：它们在整包的串行阶段运行，只验证登记表本身——
// 串行标记要被子测试继承、不能漏给兄弟测试，测试结束后要撤销；覆盖包级变量要还原，并把测试钉成串行。

func TestSerialMarkIsInheritedBySubtestsOnly(t *testing.T) {
	var pinnedSerial, childSerial, siblingSerial bool
	t.Run("pinned", func(t *testing.T) {
		markSerial(t)
		pinnedSerial = isSerial(t)
		t.Run("child", func(t *testing.T) { childSerial = isSerial(t) })
	})
	t.Run("sibling", func(t *testing.T) { siblingSerial = isSerial(t) })

	if !pinnedSerial || !childSerial {
		t.Fatalf("声明串行的测试及其子测试都应是串行: pinned=%v child=%v", pinnedSerial, childSerial)
	}
	if siblingSerial {
		t.Fatal("串行标记不应漏给兄弟测试")
	}
	if _, left := serialNames.Load(t.Name() + "/pinned"); left {
		t.Fatal("测试结束后串行标记应被撤销，否则 -count=N 重复运行时会残留")
	}
}

var overrideProbe = 7

func TestOverrideGlobalRestoresAndPinsSerial(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		overrideGlobal(t, &overrideProbe, 9)
		if overrideProbe != 9 {
			t.Fatalf("覆盖没有生效: %d", overrideProbe)
		}
		if !isSerial(t) {
			t.Fatal("改包级变量的测试应被钉成串行")
		}
	})
	if overrideProbe != 7 {
		t.Fatalf("子测试结束后应还原: %d", overrideProbe)
	}
}

func TestSerialRequestedReadsTheOptionsWithoutSideEffects(t *testing.T) {
	cases := []struct {
		name string
		opts []envOption
		want bool
	}{
		{"没有选项", nil, false},
		{"普通选项", []envOption{withExec(), withFakeClock()}, false},
		{"withSerial", []envOption{withSerial()}, true},
		{"withLogCapture 意味着串行", []envOption{withExec(), withLogCapture()}, true},
	}
	for _, tc := range cases {
		if got := serialRequested(tc.opts); got != tc.want {
			t.Errorf("%s: serialRequested = %v，期望 %v", tc.name, got, tc.want)
		}
	}
}
