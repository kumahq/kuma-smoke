package internal

import (
	"fmt"
	"github.com/spf13/cobra"
)

func CmdStdout(cmd *cobra.Command, fmtStr string, args ...interface{}) {
	_, _ = cmd.OutOrStdout().Write([]byte(fmt.Sprintf(fmtStr, args...)))
}

func CmdStdErr(cmd *cobra.Command, fmtStr string, args ...interface{}) {
	_, _ = cmd.OutOrStderr().Write([]byte(fmt.Sprintf(fmtStr, args...)))
}
