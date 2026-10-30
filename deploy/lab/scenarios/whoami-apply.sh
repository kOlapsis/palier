# Applies the plan of whoami from the command line.
echo "avant : $(whoami_image)"
stat -c 'compose.yaml %U:%G %a' /apps/whoami/compose.yaml
start=$(date +%s)
$PALIER exec -T palier /palier apply whoami || true
echo "durée : $(( $(date +%s) - start )) s"
echo "après : $(whoami_image)"
stat -c 'compose.yaml %U:%G %a' /apps/whoami/compose.yaml
grep image: /apps/whoami/compose.yaml
