# Applies the plan of whoami from the command line.
echo "before: $(whoami_image)"
stat -c 'compose.yaml %U:%G %a' /apps/whoami/compose.yaml
start=$(date +%s)
$PALIER exec -T palier /palier apply whoami </dev/null || true
echo "duration: $(( $(date +%s) - start )) s"
echo "after: $(whoami_image)"
stat -c 'compose.yaml %U:%G %a' /apps/whoami/compose.yaml
grep image: /apps/whoami/compose.yaml
