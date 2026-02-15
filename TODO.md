# TODO
## Frame collapsing / hiding for `tree`

A `--collapse` or `--hide` flag that takes a regex pattern to skip framework wrapper frames,
stitching parent to child across collapsed frames.

```
ap-query tree profile.jfr -m "IndexUpdateRunner" --depth 10 --min-pct 0.5 \
  --collapse "CoreProgressManager|Cancellation|AccessController|ProtectionDomain"
```

This way `--depth 10` covers 10 levels of meaningful frames instead of burning depth budget
on wrappers like:

```
removeDataFromIndicesForFile -> executeNonCancelableSection -> computeInNonCancelableSection
  -> computeInNonCancelableSection -> lambda -> lambda -> computeUnderProgress -> ...actual work
```

Which becomes:

```
removeDataFromIndicesForFile -> ...actual work
```
