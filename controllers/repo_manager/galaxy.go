package repo_manager

import (
	"net/url"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultConfigMapName = "ee-default-images"

// GalaxyResource has the definition and function to provision galaxy objects
type GalaxyResource struct {
	Definition ResourceDefinition
	Function   func(FunctionResources) client.Object
}

// galaxy defines the set of tasks that only galaxy deployments should run
func galaxy(resources FunctionResources) (ctrl.Result, error) {

	// ignore this func if deployment type is pulp
	if resources.Pulp.Spec.DeploymentType != "galaxy" {
		return ctrl.Result{}, nil
	}

	// ignore this method if defined to not deploy default images
	if !resources.Pulp.Spec.DeployEEDefaults {
		return ctrl.Result{}, nil
	}

	resources.Logger.V(1).Info("Running " + resources.Pulp.Spec.DeploymentType + " tasks")

	// list of galaxy resources that should be provisioned
	newResources := []GalaxyResource{
		// galaxy configmap
		{Definition: ResourceDefinition{Context: resources.Context, Type: &corev1.ConfigMap{}, Name: getConfigMapName(resources), Alias: "", ConditionType: "", Pulp: resources.Pulp}, Function: galaxyEEConfigMap},
		// galaxy cronjob
		{ResourceDefinition{resources.Context, &batchv1.CronJob{}, resources.Pulp.Name + "-ee-defaults", "", "", resources.Pulp}, galaxyEECronJob},
	}

	// create resources
	for _, resource := range newResources {
		requeue, err := resources.RepoManagerReconciler.createPulpResource(resource.Definition, resource.Function)
		if err != nil {
			return ctrl.Result{}, err
		} else if requeue {
			return ctrl.Result{Requeue: true}, nil
		}
	}

	return ctrl.Result{}, nil
}

// getConfigMapName returns the name of ConfigMap with the list of EE that should be synchronized
func getConfigMapName(resources FunctionResources) string {
	galaxyEEConfigmapName := defaultConfigMapName
	if len(resources.Pulp.Spec.EEDefaults) > 0 {
		galaxyEEConfigmapName = resources.Pulp.Spec.EEDefaults
	}

	return galaxyEEConfigmapName
}

// galaxyEEConfigMap returns a default ConfigMap with the list of default images
// that should be synced
func galaxyEEConfigMap(resources FunctionResources) client.Object {
	images := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      defaultConfigMapName,
		Namespace: resources.Namespace,
	},
		Data: map[string]string{
			"images.yaml": `quay.io:
  images-by-tag-regex:
    fedora/fedora-minimal: ^latest$
    fedora/fedora: ^latest$`,
		},
	}
	ctrl.SetControllerReference(resources.Pulp, images, resources.RepoManagerReconciler.Scheme)
	return images
}

// galaxyEECronJob returns a CronJob that will be used to trigger a sync of
// EE images from time to time
func galaxyEECronJob(resources FunctionResources) client.Object {

	// image used to run the sync
	skopeoImage := "quay.io/skopeo/stable"

	// galaxy image registry host
	rootURL, _ := url.Parse(getRootURL(resources))

	successfulHistory := int32(1)
	failedHistory := int32(2)

	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.Pulp.Name + "-ee-defaults",
			Namespace: resources.Pulp.Namespace,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   "*/2 * * * *",
			SuccessfulJobsHistoryLimit: &successfulHistory,
			FailedJobsHistoryLimit:     &failedHistory,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resources.Pulp.Name + "-ee-defaults",
					Namespace: resources.Pulp.Namespace,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Name:      resources.Pulp.Name + "-ee-defaults",
							Namespace: resources.Pulp.Namespace,
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{{
								Name:            "skopeo",
								Image:           skopeoImage,
								ImagePullPolicy: corev1.PullAlways,
								Env: []corev1.EnvVar{
									{Name: "USERNAME", Value: "admin"},
									{Name: "PASSWORD",
										ValueFrom: &corev1.EnvVarSource{
											SecretKeyRef: &corev1.SecretKeySelector{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: resources.Pulp.Spec.AdminPasswordSecret,
												},
												Key: "password",
											},
										},
									},
								},
								Args: []string{
									"--debug", "sync", "--dest", "docker", "--src", "yaml", "--retry-times", "3", "--dest-creds", "$(USERNAME):$(PASSWORD)", "--dest-tls-verify=false", "--keep-going", "/images.yaml", rootURL.Host + "/",
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "ee-default-images",
										MountPath: "/images.yaml",
										SubPath:   "images.yaml",
										ReadOnly:  true,
									},
								},
							}},
							Volumes: []corev1.Volume{
								{
									Name: "ee-default-images",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: getConfigMapName(resources),
											},
											Items: []corev1.KeyToPath{
												{Key: "images.yaml", Path: "images.yaml"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	ctrl.SetControllerReference(resources.Pulp, cronJob, resources.RepoManagerReconciler.Scheme)
	return cronJob
}
