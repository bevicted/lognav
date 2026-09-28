package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bevicted/lognav/docs"
	"github.com/bevicted/lognav/internal/build"
	"github.com/bevicted/lognav/internal/openurl"
)

// openBrowser is a package var so command tests can substitute a hermetic stub.
var openBrowser = openurl.Open

// docsURL builds the GHE permalink for a docs topic at the given ref. An empty
// topic resolves to the documentation index page.
func docsURL(repoURL, ref, topic string) string {
	name := topic
	if name == "" {
		name = "documentation"
	}
	return repoURL + "/blob/" + ref + "/docs/user/" + name + ".md"
}

type embeddedDocument struct {
	topic string
	path  string
}

// embeddedUserDocuments returns user documentation in filename order.
func embeddedUserDocuments(filesystem fs.FS) ([]embeddedDocument, error) {
	paths, err := fs.Glob(filesystem, "user/*.md")
	if err != nil {
		return nil, fmt.Errorf("list embedded user documentation: %w", err)
	}
	sort.Strings(paths)

	documents := make([]embeddedDocument, 0, len(paths))
	for _, path := range paths {
		documents = append(documents, embeddedDocument{
			topic: strings.TrimSuffix(pathpkg.Base(path), ".md"),
			path:  path,
		})
	}
	return documents, nil
}

// resolveDocumentationTopic returns the embedded document selected by topic.
func resolveDocumentationTopic(filesystem fs.FS, topic string) (embeddedDocument, error) {
	if topic == "" {
		topic = "documentation"
	}
	documents, err := embeddedUserDocuments(filesystem)
	if err != nil {
		return embeddedDocument{}, err
	}
	for _, document := range documents {
		if document.topic == topic {
			return document, nil
		}
	}
	return embeddedDocument{}, fmt.Errorf("unknown documentation topic %q; use --greppable to discover embedded user documentation", topic)
}

// writePrintableDocument writes one embedded document without altering its bytes.
func writePrintableDocument(w io.Writer, filesystem fs.FS, topic string) error {
	document, err := resolveDocumentationTopic(filesystem, topic)
	if err != nil {
		return err
	}
	contents, err := fs.ReadFile(filesystem, document.path)
	if err != nil {
		return fmt.Errorf("read embedded documentation %q: %w", document.path, err)
	}
	_, err = w.Write(contents)
	return err
}

// writeGreppableDocumentation writes embedded user-document lines as filename:line:text.
func writeGreppableDocumentation(w io.Writer, filesystem fs.FS) error {
	documents, err := embeddedUserDocuments(filesystem)
	if err != nil {
		return err
	}

	for _, document := range documents {
		contents, err := fs.ReadFile(filesystem, document.path)
		if err != nil {
			return fmt.Errorf("read embedded documentation %q: %w", document.path, err)
		}
		if len(contents) == 0 {
			continue
		}
		lines := bytes.Split(contents, []byte{'\n'})
		if contents[len(contents)-1] == '\n' {
			lines = lines[:len(lines)-1]
		}
		for lineNumber, line := range lines {
			if _, err := fmt.Fprintf(w, "%s:%d:%s\n", pathpkg.Base(document.path), lineNumber+1, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateDocsArgs rejects incompatible output modes and unknown topics.
func validateDocsArgs(filesystem fs.FS, printDocument, greppableDocumentation bool, args []string) error {
	if printDocument && greppableDocumentation {
		return WithExit(ExitUsage, errors.New("--print and --greppable cannot be used together"))
	}
	if greppableDocumentation && len(args) > 0 {
		return WithExit(ExitUsage, errors.New("--greppable does not accept a topic"))
	}
	if len(args) > 1 {
		return WithExit(ExitUsage, errors.New("docs accepts at most one topic"))
	}
	if greppableDocumentation {
		return nil
	}
	topic := ""
	if len(args) > 0 {
		topic = strings.ToLower(args[0])
	}
	if _, err := resolveDocumentationTopic(filesystem, topic); err != nil {
		return WithExit(ExitUsage, err)
	}
	return nil
}

func initDocs() *cobra.Command {
	var noOpen, printDocument, greppableDocumentation bool
	documents, err := embeddedUserDocuments(docs.Docs)
	if err != nil {
		panic(fmt.Sprintf("list embedded user documentation: %v", err))
	}
	topics := make([]string, len(documents))
	for i, document := range documents {
		topics[i] = document.topic
	}

	cmd := &cobra.Command{
		Use:     "docs [topic]",
		Aliases: []string{"man", "manual"},
		Short:   "Open or print lognav documentation",
		Long: "Open lognav documentation on GitHub Enterprise, pinned to this binary's version.\n\n" +
			"Without a topic, opens the documentation index. A named topic is lowercased and must match an embedded user-page filename; an unknown topic is a usage error before a URL is printed or browser launched. The URL is always printed. With `--print`, no topic selects the index and no URL is emitted. `--greppable` includes every embedded user page and blank line in stable filename and line order. It accepts no topic and cannot be combined with `--print`.\n\nAvailable topics: " + strings.Join(topics, ", ") + ".",
		Example: "  lognav docs\n  lognav docs authentication\n  lognav docs --no-open\n  lognav docs authentication --print\n  lognav docs --greppable | rg authentication",
		Args: func(_ *cobra.Command, args []string) error {
			return validateDocsArgs(docs.Docs, printDocument, greppableDocumentation, args)
		},
		ValidArgsFunction: completeDocumentationTopics,
		RunE: func(cmd *cobra.Command, args []string) error {
			if greppableDocumentation {
				return writeGreppableDocumentation(cmd.OutOrStdout(), docs.Docs)
			}

			topic := ""
			if len(args) > 0 {
				topic = strings.ToLower(args[0])
			}
			document, err := resolveDocumentationTopic(docs.Docs, topic)
			if err != nil {
				return err
			}
			if printDocument {
				return writePrintableDocument(cmd.OutOrStdout(), docs.Docs, document.topic)
			}

			url := docsURL(build.RepoURL(), build.VersionRef(), document.topic)
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), url); err != nil {
				return err
			}
			if noOpen {
				return nil
			}
			if err := openBrowser(cmd.Context(), url); err != nil {
				slog.Error("open documentation browser", "url", url, "error", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&noOpen, "no-open", "n", false, "Print the URL without opening a browser")
	cmd.Flags().BoolVarP(&printDocument, "print", "p", false, "Print embedded documentation without opening a browser")
	cmd.Flags().BoolVarP(&greppableDocumentation, "greppable", "g", false, "Print embedded user documentation as filename:line:text")
	return cmd
}

// completeDocumentationTopics returns embedded user-document stems.
func completeDocumentationTopics(_ *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	documents, err := embeddedUserDocuments(docs.Docs)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var topics []string
	for _, document := range documents {
		if strings.HasPrefix(document.topic, prefix) {
			topics = append(topics, document.topic)
		}
	}
	return topics, cobra.ShellCompDirectiveNoFileComp
}
