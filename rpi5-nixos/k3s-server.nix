{ config, pkgs, lib, ... }:

{
  imports = [
    ./k3s-common.nix
    ./k3s-token-secrets.nix
  ];

  environment.etc."rancher/k3s/server/manifests/kube-vip.yaml".text = ''
    apiVersion: v1
    kind: ServiceAccount
    metadata:
      name: kube-vip
      namespace: kube-system
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata:
      name: kube-vip
      namespace: kube-system
    rules:
    - apiGroups: ["coordination.k8s.io"]
      resources: ["leases"]
      verbs: ["get", "create", "update", "list"]
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: RoleBinding
    metadata:
      name: kube-vip
      namespace: kube-system
    roleRef:
      apiGroup: rbac.authorization.k8s.io
      kind: Role
      name: kube-vip
    subjects:
    - kind: ServiceAccount
      name: kube-vip
      namespace: kube-system
    ---
    apiVersion: apps/v1
    kind: DaemonSet
    metadata:
      name: kube-vip
      namespace: kube-system
      labels:
        app: kube-vip
    spec:
      selector:
        matchLabels:
          app: kube-vip
      template:
        metadata:
          labels:
            app: kube-vip
        spec:
          nodeSelector:
            node-role.kubernetes.io/control-plane: "true"
          tolerations:
          - effect: NoSchedule
            key: node-role.kubernetes.io/control-plane
            operator: Exists
          - effect: NoSchedule
            key: node-role.kubernetes.io/master
            operator: Exists
          - effect: NoExecute
            key: CriticalAddonsOnly
            operator: Exists
          hostNetwork: true
          dnsPolicy: ClusterFirstWithHostNet
          serviceAccountName: kube-vip
          containers:
          - name: kube-vip
            image: ghcr.io/kube-vip/kube-vip@sha256:a48dc8d85c7d36876dcc8c0661ac225603936065e51e2e3a92978f036877dcb2
            args: ["manager"]
            env:
            - name: KUBERNETES_SERVICE_HOST
              value: "127.0.0.1"
            - name: KUBERNETES_SERVICE_PORT
              value: "6443"
            - name: vip_arp
              value: "true"
            - name: vip_interface
              value: "end0"
            - name: port
              value: "6443"
            - name: vip_address
              value: "10.0.0.200"
            - name: vip_cidr
              value: "32"
            - name: cp_enable
              value: "true"
            - name: cp_namespace
              value: "kube-system"
            - name: svc_enable
              value: "false"
            - name: vip_leaderelection
              value: "true"
            - name: vip_leaseduration
              value: "15"
            - name: vip_renewdeadline
              value: "10"
            - name: vip_retryperiod
              value: "2"
            securityContext:
              capabilities:
                add: ["NET_ADMIN", "NET_RAW"]
  '';

  services.k3s = {
    enable = true;
    role = "server";
    tokenFile = "/etc/rancher/k3s/token";
    extraFlags = toString [
      "--flannel-iface=end0"
      "--tls-san 10.0.0.200"
      "--disable servicelb"
      "--disable traefik"
      "--write-kubeconfig-mode 644"
      "--node-taint CriticalAddonsOnly=true:NoExecute"
    ];
  };
  
  systemd.services.k3s-kubeconfig-copy = {
    description = "Copy k3s kubeconfig to user directory";
    after = [ "k3s.service" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      mkdir -p /home/pi/.kube
      cp /etc/rancher/k3s/k3s.yaml /home/pi/.kube/config
      chown -R pi:users /home/pi/.kube
      chmod 600 /home/pi/.kube/config
    '';
  };

  systemd.services.k3s-apply-manifests = {
    description = "Apply k3s manifests";
    after = [ "k3s.service" ];
    wants = [ "k3s.service" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      Restart = "on-failure";
      RestartSec = "10s";
    };
    script = ''
      # Wait for k3s to be ready
      until ${pkgs.k3s}/bin/kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml get nodes &>/dev/null; do
        echo "Waiting for k3s to be ready..."
        sleep 5
      done
      
      # Apply the kube-vip manifest
      ${pkgs.k3s}/bin/kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml apply -f /etc/rancher/k3s/server/manifests/kube-vip.yaml
    '';
  };
}
