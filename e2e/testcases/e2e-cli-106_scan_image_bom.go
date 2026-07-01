package testcases

// E2E-CLI-106 - KICS scan command with --image-bom
// should export a CycloneDX bill of materials of the Docker images found in the scan
func init() { //nolint
	testSample := TestCase{
		Name: "should export a bill of materials of the docker images found in the scan [E2E-CLI-106]",
		Args: args{
			Args: []cmdArgs{
				[]string{"scan",
					"-p", "/path/e2e/fixtures/samples/image_bom",
					"--image-bom",
					"-o", "/path/e2e/output",
					"--ignore-on-exit", "results",
				},
			},
			ExpectedResult: []ResultsValidation{
				{
					ResultsFile:    "E2E_CLI_106_RESULT",
					ResultsFormats: []string{"image-bom"},
				},
			},
		},
		WantStatus: []int{0},
	}

	Tests = append(Tests, testSample)
}
