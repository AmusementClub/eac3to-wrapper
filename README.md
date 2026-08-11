eac3to-wrapper
==============

eac3to-wrapper aims to fix eac3to's long standing [bug 288](http://bugs.madshi.net/view.php?id=288).

Build
-----

Build on Windows with Go:

```bat
go build -ldflags "-X main.version=VERSION" -o eac3to-wrapper.exe
```

Prebuilt Windows executables can be placed anywhere before deployment.

Installation in OKEGui
----------------------

Copy `eac3to-wrapper.exe` into OKEGui's existing `tools\eac3to` directory.
Do not rename or replace the original `eac3to.exe`.

Assuming the built wrapper is `D:\eac3to-wrapper.exe`:

```bat
copy D:\eac3to-wrapper.exe path\to\OKEGui\tools\eac3to\eac3to-wrapper.exe
```

The wrapper locates the original `eac3to.exe` in its own directory and locates
`mkvextract.exe` and `mkvmerge.exe` in `..\mkvtoolnix`. OKEGui must be
configured to invoke `eac3to-wrapper.exe`; the original `eac3to.exe` remains
available for direct use.

To restore the original setup, remove `eac3to-wrapper.exe` or change OKEGui
back to `eac3to.exe`.

When invoked with OKEGui, the wrapper writes its diagnostic log under
`OKEGui\log\EAC_YYMMDD-HHMM_PID.log`.

Non-ASCII paths on Windows
--------------------------

Windows PowerShell can decode UTF-8 console input as the active ANSI code
page before it starts a native program. This is especially visible on Chinese
Windows installations (CP936) with Japanese source paths: legacy eac3to may
successfully analyse the source, then fail with `This audio conversion is not
supported.`.

The wrapper handles this by creating a per-invocation temporary ASCII-only
NTFS junction for each existing absolute argument path containing non-ASCII
characters. It passes the ASCII alias to legacy eac3to, then removes the
junction after eac3to exits. The wrapper itself uses Unicode-aware Windows APIs
for the alias setup and cleanup.

This prevents affected paths from reaching eac3to's legacy command-line parser,
so installing the wrapper removes the need to change the console code page to
UTF-8. Creating the temporary junction requires ordinary write permission in
the current user's temporary directory on a local NTFS volume.

Limitations
-----------

It only recognizes the following forms:

1. `eac3to input.mkv TID1:out1.flac TID2:out2.sup ...`
2. `eac3to input.mkv TID1: out1.flac TID2: out2.sup ...`

All unrecognized forms will be passed through to original eac3to in verbatim.

Due to complications arising from Windows' support of command line arguments, the first form is proactively split into the second form.
