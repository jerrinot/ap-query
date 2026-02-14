# TODO

## Trace command — follow the hottest path to the leaf

A new `trace` (or `spine`) command that follows the single hottest child at each level
all the way down to the leaf, with no depth limit.

```
ap-query trace profile.jfr -t "DefaultDispatcher" -m "IndexUpdateRunner" --min-pct 0.5
```

Output:

```
[25.7%] IndexUpdateRunner.indexOneFileHandleExceptions
  [25.2%] FileBasedIndexImpl.removeDataFromIndicesForFile
    [24.7%] FileBasedIndexImpl.removeFileDataFromIndices
      [24.5%] SingleIndexValueRemover.remove
        [23.5%] MapReduceIndex.updateWith
          [14.8%] MapInputDataDiffBuilder.differentiate
            [14.6%] EmptyInputDataDiffBuilder.processAllKeyValuesAsRemoved
              [11.4%] ShardedIndexStorage.removeAllValues
                [9.5%] VfsAwareMapIndexStorage.removeAllValues
                  [2.4%] LockedCacheWrapper.read  <- self=1.4%
```

At branching points (where the hottest child doesn't account for all the parent's samples),
a `--branches` flag could expand siblings above `--min-pct`.

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
