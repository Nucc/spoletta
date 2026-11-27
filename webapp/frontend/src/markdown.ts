// Telling Markdown text apart, and saying it plainly. Wherever a run's
// objects or a record's fields are shown (Data.tsx), text in a field typed
// Markdown (attribute_types.go) is rendered as Markdown, and other text
// only when it reads as Markdown; a cell or a card too small to render it
// shows the plain text it reads as. Checked by scripts/markdown.ts.

// Lines that only Markdown starts with: headings, list items, quotes, fences
// and table rules.
const markdownLine = /^(#{1,6} \S|\s*[-*+] \S|\s*\d+[.)] \S|> |```|\|?\s*:?-{3,}:?\s*\|)/m
// Inline marks: bold, links and code.
const markdownInline = /\*\*\S[^*\n]*\*\*|\[[^\]\n]+\]\([^)\s]+\)|`[^`\n]+`/g

// Whether the text reads as Markdown: a heading, list, quote, fence or table,
// or a few inline marks, so a stray asterisk or backtick doesn't count.
export function isMarkdown(text: string): boolean {
  return markdownLine.test(text) || (text.match(markdownInline)?.length ?? 0) >= 2
}

// Markdown as the text it reads as, on one line: its marks taken out, a
// link or an image as its text, a table's cells apart.
export function plainText(markdown: string): string {
  return (
    markdown
      // Fences, keeping the code they hold, and table rules.
      .replace(/^[ \t]*(```|~~~).*$/gm, '')
      .replace(/^[ \t]*\|?[ \t]*:?-{3,}:?[ \t]*(\|[ \t]*:?-{3,}:?[ \t]*)*\|?[ \t]*$/gm, '')
      // Headings, quotes, list items, ticked or not, and rules.
      .replace(/^[ \t]{0,3}(#{1,6}[ \t]+|>[ \t]?)/gm, '')
      .replace(/^[ \t]*([-*+]|\d+[.)])[ \t]+(\[[ xX]\][ \t]+)?/gm, '')
      .replace(/^[ \t]*([-*_][ \t]*){3,}$/gm, '')
      // Images and links as their text.
      .replace(/!?\[([^\]\n]*)\]\([^)\n]*\)/g, '$1')
      // Emphasis, struck text and code, left as what they mark; an
      // underscore inside a word, as in snake_case, marks nothing.
      .replace(/(\*\*|__)(\S(?:.*?\S)?)\1/g, '$2')
      .replace(/(?<![\w*])\*(\S(?:[^*\n]*?\S)?)\*(?![\w*])/g, '$1')
      .replace(/(?<![\w_])_(\S(?:[^_\n]*?\S)?)_(?![\w_])/g, '$1')
      .replace(/~~(\S(?:.*?\S)?)~~/g, '$1')
      .replace(/`([^`\n]*)`/g, '$1')
      // A table's rows as their cells, apart.
      .replace(/^[ \t]*\|(.*)\|[ \t]*$/gm, (_, cells: string) => cells.split('|').map((c) => c.trim()).join(' · '))
      .replace(/\s+/g, ' ')
      .trim()
  )
}
