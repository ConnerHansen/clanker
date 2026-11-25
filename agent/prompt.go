package agent

var formatStringPr = `Analyze this git diff and produce a concise PR summary as an expert engineer. Offer no additional commentary or statements. Do not ask any followup questions and do not offer suggestions. Produce no more than 5 sentences for the summary and no more than 5 bullet points. Follow this format:

## Summary
{{Summary of changes}}

**Changes:**
* {{Bullet list of most relevant changes}}

The git commit log and diff are attached below:

Git Commit Log:
"""
%s
"""

Git Diff:
"""
%s
"""
`
