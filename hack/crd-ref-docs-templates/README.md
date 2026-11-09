# crd-ref-docs markdown templates

Copied verbatim from [elastic/crd-ref-docs](https://github.com/elastic/crd-ref-docs)
`v0.3.0`, `templates/markdown/`, which is Apache-2.0 licensed. `--templates-dir`
replaces the generator's embedded template set wholesale, so all four files have
to be present even though only one is modified.

Both local changes are in `type.tpl`:

1. The Validation column renders each entry raw instead of through
   `markdownRenderFieldDoc`, which escapes `{` and `}` unconditionally.
   Validation entries are already Markdown — a pattern arrives wrapped in
   backticks, and Markdown does not process escapes inside a code span, so
   `^([0-9a-fA-F]{2}:){5}...` reached the page as
   `^([0-9a-fA-F]\{2\}:)\{5\}...`. Pipes are still escaped: an unescaped `|` in
   a pattern such as `^TCP|UDP|SCTP$` splits the table row.

2. An alias type that carries its own validation is rendered even when
   `ShouldRenderType` rejects it. That function requires a GVK or an in-package
   reference, which hides a type whose only users live in other packages —
   `NetworkSelectors` and its 1-5 item limit, in our case, since the groups that
   embed it are rendered by separate runs.

When bumping the pinned version in `hack/update-api-reference.sh`, re-copy these
files and re-apply both changes.
