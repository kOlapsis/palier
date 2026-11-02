# The plan of whoami names a target that the registry does not have, as if the tag had been removed after planning.
target=$(sql "SELECT json_extract(body, '$.steps[0].changes[0].to') FROM plans WHERE stack = 'whoami'")
[ -n "$target" ] || { echo "no plan for whoami: run palier plan whoami first"; exit 1; }
echo "plan: whoami to $target, replaced by v1.99.0"
sql "UPDATE plans SET body = replace(body, '\"$target\"', '\"v1.99.0\"') WHERE stack = 'whoami'"
echo "before: $(whoami_image)"
start=$(date +%s)
$PALIER exec -T palier /palier apply whoami </dev/null || true
echo "duration: $(( $(date +%s) - start )) s"
echo "after: $(whoami_image)"
grep image: /apps/whoami/compose.yaml
sql "UPDATE plans SET body = replace(body, '\"v1.99.0\"', '\"$target\"') WHERE stack = 'whoami'"
