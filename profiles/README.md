  Build & Run                                                                                                     
                                                                                                                
  # Build the binary
  go build -o homerun2-k8s-pitcher .

  # Run with the homerun2 profile (using your kubeconfig)
  ./homerun2-k8s-pitcher --profile profiles/homerun2.yaml --kubeconfig ~/.kube/config

  It will:
  1. Load the profile
  2. Resolve the auth token from the pitcher-auth secret in homerun2-flux namespace
  3. Health-check the omni-pitcher at https://pitcher.movie-scripts2.sthings-vsphere.labul.sva.de
  4. Start collectors (Node/60s, Pod/30s, Event/15s) and informers (pods, deployments)

  Generate test events

  # Apply test manifests to trigger add events
  kubectl apply -f tests/test-events.yaml

  # Scale deployment to trigger update events
  kubectl -n homerun2-flux scale deployment test-pitcher-deploy --replicas=2

  # Delete to trigger delete events
  kubectl delete -f tests/test-events.yaml

  Prerequisites

  Make sure the pitcher-auth secret exists in homerun2-flux:

  # Check if it exists
  kubectl get secret pitcher-auth -n homerun2-flux

  # Create if needed
  kubectl create secret generic pitcher-auth -n homerun2-flux --from-literal=token=<your-token>

  The token must match what the homerun2-omni-pitcher expects for X-Auth-Token authentication.