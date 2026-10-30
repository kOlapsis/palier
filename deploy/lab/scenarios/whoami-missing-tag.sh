# The plan of whoami names a target that the registry does not have, as if the tag had been removed after planning.
target=$(sql "SELECT json_extract(body, '$.steps[0].changes[0].to') FROM plans WHERE stack = 'whoami'")
[ -n "$target" ] || { echo "pas de plan pour whoami : lancer d'abord palier plan whoami"; exit 1; }
echo "plan : whoami vers $target, remplacé par v1.99.0"
sql "UPDATE plans SET body = replace(body, '\"$target\"', '\"v1.99.0\"') WHERE stack = 'whoami'"
echo "avant : $(whoami_image)"
start=$(date +%s)
$PALIER exec -T palier /palier apply whoami || true
echo "durée : $(( $(date +%s) - start )) s"
echo "après : $(whoami_image)"
grep image: /apps/whoami/compose.yaml
sql "UPDATE plans SET body = replace(body, '\"v1.99.0\"', '\"$target\"') WHERE stack = 'whoami'"
