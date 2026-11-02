# Applies the whole GitLab path, stop after stop, and checks that the seeded project and account are still there.
echo "before:"; gitlab_state
start=$(date +%s)
apply_and_report gitlab
echo "duration: $(( $(date +%s) - start )) s"
echo "after:"; gitlab_state
grep image: /apps/gitlab/compose.yaml
curl -s -o /dev/null -w "gitlab readiness %{http_code}\n" http://127.0.0.1:8929/-/readiness
