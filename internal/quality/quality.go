package quality

import (
	"fmt"
)

const help = `Usage: goqual <command> [options]

Commands:
  crap       Run a CRAP report
  mutate     Run mutation testing for one Go source file
  help       Print this help message

Run "goqual <command> --help" for command-specific options.`

func Run(args []string) (int, error) {
	if len(args) == 0 {
		fmt.Println(help)
		return 0, nil
	}

	command := args[0]
	rest := args[1:]
	switch command {
	case "help", "-h", "--help":
		fmt.Println(help)
		return 0, nil
	case "crap":
		return runCRAP(rest)
	case "mutate":
		return runMutate(rest)
	default:
		return 1, fmt.Errorf("unknown command %q\n\n%s", command, help)
	}
}
