package cmd

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed instructions
var instructionFiles embed.FS

type instructionTopic struct {
	name      string
	fragments []string
}

var instructionTopics = []instructionTopic{
	{name: "config", fragments: []string{"instructions/config.md"}},
	{name: "query", fragments: []string{"instructions/query.md", "instructions/query-completion.md"}},
	{name: "investigation", fragments: []string{"instructions/query.md", "instructions/investigation.md"}},
}

const instructionWayfinder = "`lognav instruct` is an offline router. Before acting, load the one smallest matching topic; a topic can contain one or more procedures:\n\n" +
	"- Reference question with a known page: `lognav docs <topic> --print` (for example, `lognav docs authentication --print`).\n" +
	"- Reference question without a known page: `lognav docs --greppable | rg 'terms'`, then print the matching filename's topic.\n" +
	"- Configuration action: `lognav instruct config`.\n" +
	"- One-shot Dataprime fetch and outcome: `lognav instruct query`.\n" +
	"- Evidence-based record analysis: `lognav instruct investigation`.\n\n" +
	"Do not load redundant topics. `investigation` already includes the Query procedure; do not load `query` separately. Run another command only for an independent requested outcome. Ask the user before acting when the goal remains ambiguous.\n"

// initInstruct creates the offline, config-independent agent instruction router.
func initInstruct() *cobra.Command {
	return &cobra.Command{
		Use:               "instruct [config|query|investigation]",
		Short:             "Print zero-context instructions for agents",
		Long:              "Print offline, config-independent instructions for a local zero-context agent. With no topic, print a short wayfinder; otherwise load exactly one smallest matching topic from `config`, `query`, or `investigation`; a topic can contain one or more procedures. `query` covers one fetch and outcome. `investigation` already includes the Query procedure, then adds retained-evidence analysis; do not load both. This command never reads config, checks credentials, reads snapshots, or contacts the network.",
		Example:           "  lognav instruct\n  lognav instruct query\n  lognav instruct investigation",
		Args:              instructionArgs,
		ValidArgs:         instructionTopicNames(),
		ValidArgsFunction: completeInstructionTopics,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				_, err := fmt.Fprint(cmd.OutOrStdout(), instructionWayfinder)
				return err
			}
			output, err := renderInstruction(args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), output)
			return err
		},
	}
}

// instructionArgs validates one exact instruction topic.
func instructionArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(args) != 1 {
		return WithExit(ExitUsage, fmt.Errorf("instruct accepts one topic: %s", instructionTopicList()))
	}
	if !isInstructionTopic(args[0]) {
		return WithExit(ExitUsage, fmt.Errorf("unknown instruction topic %q; choose one of: %s", args[0], instructionTopicList()))
	}
	return nil
}

func completeInstructionTopics(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var matches []string
	for _, name := range instructionTopicNames() {
		if strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

func instructionTopicNames() []string {
	names := make([]string, len(instructionTopics))
	for i, topic := range instructionTopics {
		names[i] = topic.name
	}
	return names
}

func instructionTopicList() string {
	return strings.Join(instructionTopicNames(), ", ")
}

func isInstructionTopic(name string) bool {
	for _, topic := range instructionTopics {
		if topic.name == name {
			return true
		}
	}
	return false
}

// renderInstruction returns the ordered authored procedures for one exact topic.
func renderInstruction(topicName string) (string, error) {
	topic, ok := findInstructionTopic(topicName)
	if !ok {
		return "", fmt.Errorf("unknown instruction topic %q", topicName)
	}
	return renderInstructionFragments(topic.fragments)
}

func renderInstructionFragments(paths []string) (string, error) {
	fragments := make([]string, 0, len(paths))
	for _, path := range paths {
		contents, err := fs.ReadFile(instructionFiles, path)
		if err != nil {
			return "", fmt.Errorf("read embedded instruction fragment %q: %w", path, err)
		}
		fragments = append(fragments, strings.TrimSpace(string(contents)))
	}
	return strings.Join(fragments, "\n\n") + "\n", nil
}

func findInstructionTopic(name string) (instructionTopic, bool) {
	for _, topic := range instructionTopics {
		if topic.name == name {
			return topic, true
		}
	}
	return instructionTopic{}, false
}
