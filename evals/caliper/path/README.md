# PATH tools for Caliper Q1

`gh` here is the code-host stand-in for `evals/caliper/q1`. It lives outside
that directory on purpose: `sandbox.extra_path` points here so `which gh`
does not resolve inside the directory that holds `check.py`,
`build-subject.sh`, and the hidden tests.
