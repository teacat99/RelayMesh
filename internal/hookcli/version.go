package hookcli

// SetBuildVersion 由 main 包在 init 时注入编译版本。
func SetBuildVersion(v string) {
	if v != "" {
		version = v
	}
}
