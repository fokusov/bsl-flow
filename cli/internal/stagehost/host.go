package stagehost

import (
	"context"
	"io"
	"os"

	"bsl-flow/cli/internal/repository"
)

const providerInputLimit int64 = 16 << 20

// RunProvider is the `bsl-flow __provider` entry: one canonical provider
// input document on stdin, one canonical observation on stdout, the legacy
// process error taxonomy on stderr. It never launches PowerShell.
func RunProvider(ctx context.Context, deps Deps, stdin io.Reader, stdout, stderr io.Writer) int {
	if deps.RunProcess == nil {
		deps.RunProcess = nil
	}
	data, err := readBoundedInput(stdin)
	if err != nil {
		writeProviderError(stderr, err)
		return 1
	}
	object, err := repository.DecodeObject(data)
	if err != nil {
		writeProviderError(stderr, invalidf("provider input must be a JSON object."))
		return 1
	}
	input, err := validateProviderInput(deps, object)
	if err != nil {
		writeProviderError(stderr, err)
		return 1
	}
	var result map[string]any
	if ctx == nil {
		ctx = context.Background()
	}
	switch input.operation {
	case "measure":
		result, err = providerMeasure(ctx, deps, input)
	case "execute":
		result, err = providerExecute(ctx, deps, input)
	}
	if err != nil {
		writeProviderError(stderr, err)
		return 1
	}
	observation, err := repository.Canonical(result)
	if err != nil {
		writeProviderError(stderr, invalidf("%v", err))
		return 1
	}
	if _, err := stdout.Write(observation); err != nil {
		writeProviderError(stderr, blockedf("%v", err))
		return 1
	}
	return 0
}

func writeProviderError(stderr io.Writer, err error) {
	message := err.Error()
	if message == "" {
		message = "BF_BLOCKED: native provider failed."
	}
	_, _ = stderr.Write([]byte(message))
	_, _ = stderr.Write([]byte("\n"))
}

// readBoundedInput mirrors Read-BFNativeProviderInputBytes: the 16 MiB input
// bound is part of the wire contract, so the provider reads exactly the
// bounded stream and rejects anything larger before decoding.
func readBoundedInput(stdin io.Reader) ([]byte, error) {
	const limit = int64(16 << 20)
	data, err := io.ReadAll(io.LimitReader(stdin, limit+1))
	if err != nil {
		return nil, blockedf("%v", err)
	}
	if int64(len(data)) > limit {
		return nil, invalidf("provider input exceeds the 16 MiB limit.")
	}
	if len(data) == 0 {
		return nil, invalidf("provider input is empty.")
	}
	return data, nil
}

var _ = os.Environ
