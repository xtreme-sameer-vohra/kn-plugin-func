package function

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// FunctionFile is the file used for the serialized form of a Function.
const FunctionFile = "func.yaml"

type Function struct {
	// Root on disk at which to find/create source and config files.
	Root string `yaml:"-"`

	// Name of the Function.  If not provided, path derivation is attempted when
	// requried (such as for initialization).
	Name string `yaml:"name" jsonschema:"pattern=^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"`

	// Namespace into which the Function is deployed on supported platforms.
	Namespace string `yaml:"namespace"`

	// Runtime is the language plus context.  nodejs|go|quarkus|rust etc.
	Runtime string `yaml:"runtime"`

	// Template for the Function.
	Template string `yaml:"-"`

	// Registry at which to store interstitial containers, in the form
	// [registry]/[user].
	Registry string

	// Optional full OCI image tag in form:
	//   [registry]/[namespace]/[name]:[tag]
	// example:
	//   quay.io/alice/my.function.name
	// Registry is optional and is defaulted to DefaultRegistry
	// example:
	//   alice/my.function.name
	// If Image is provided, it overrides the default of concatenating
	// "Registry+Name:latest" to derive the Image.
	Image string `yaml:"image"`

	// SHA256 hash of the latest image that has been built
	ImageDigest string `yaml:"imageDigest"`

	// Builder represents the CNCF Buildpack builder image for a function
	Builder string `yaml:"builder"`

	// Map containing known builders.
	// e.g. { "jvm": "docker.io/example/quarkus-jvm-builder" }
	Builders map[string]string `yaml:"builders"`

	// Optional list of buildpacks to use when building the function
	Buildpacks []string `yaml:"buildpacks"`

	// List of volumes to be mounted to the function
	Volumes []Volume `yaml:"volumes"`

	// Build Env variables to be set
	BuildEnvs []Env `yaml:"buildEnvs"`

	// Env variables to be set
	Envs []Env `yaml:"envs"`

	// Map containing user-supplied annotations
	// Example: { "division": "finance" }
	Annotations map[string]string `yaml:"annotations"`

	// Options to be set on deployed function (scaling, etc.)
	Options Options `yaml:"options"`

	// Map of user-supplied labels
	Labels []Label `yaml:"labels"`

	// Health endpoints specified by the language pack
	HealthEndpoints HealthEndpoints `yaml:"healthEndpoints"`

	// Created time is the moment that creation was successfully completed
	// according to the client which is in charge of what constitutes being
	// fully "Created" (aka initialized)
	Created time.Time
}

// NewFunction loads a Function from a path on disk.
// Errors are returned if the path is not valid, the serialized field could not
// be accessed, or if the contents of the file could not be unmarshaled into a
// Function.  A valid path with no associated FunctionFile is not an error but
// rather returns a Function with static defaults set, and will return false
// from .Initialized().
func NewFunction(root string) (f Function, err error) {
	// NewFunction is essentially a convenience/decorator over the more fully-
	// featured constructor which takes a full function object as defaults.
	return NewFunctionFromDefaults(Function{Root: root})
}

// NewFunctionFromDefaults is equivalent to calling NewFunction, but will use
// the provided function as defaults.
func NewFunctionFromDefaults(f Function) (Function, error) {
	var err error
	if f.Runtime == "" {
		f.Runtime = DefaultRuntime
	}
	if f.Template == "" {
		f.Template = DefaultTemplate
	}
	if f.Root, err = filepath.Abs(f.Root); err != nil {
		return f, err
	}
	if f.Name == "" {
		f.Name = nameFromPath(f.Root)
	}
	return unmarshalFunction(f)
}

// nameFromPath returns the default name for a Function derived from a path.
// This consists of the last directory in the given path, if derivable (empty
// paths, paths consisting of all slashes, etc. return the zero value "")
func nameFromPath(path string) string {
	pathParts := strings.Split(strings.TrimRight(path, string(os.PathSeparator)), string(os.PathSeparator))
	return pathParts[len(pathParts)-1]
	/* the above may have edge conditions as it assumes the trailing value
	 * is a directory name.  If errors are encountered, we _may_ need to use the
	 * inbuilt logic in the std lib and either check if the path indicated is a
	 * directory (appending slash) and then run:
					 base := filepath.Base(filepath.Dir(path))
					 if base == string(os.PathSeparator) || base == "." {
									 return "" // Consider it underivable: string zero value
					 }
					 return base
	*/
}

// Write aka (save, serialize, marshal) the Function to disk at its path.
func (f Function) Write() (err error) {
	path := filepath.Join(f.Root, FunctionFile)
	var bb []byte
	if bb, err = yaml.Marshal(&f); err != nil {
		return
	}
	// TODO: open existing file for writing, such that existing permissions
	// are preserved.
	return ioutil.WriteFile(path, bb, 0644)
}

// Initialized returns if the Function has been initialized.
// Any errors are considered failure (invalid or inaccessible root, config file, etc).
func (f Function) Initialized() bool {
	return !f.Created.IsZero()
}

// Built indicates the Function has been built.  Does not guarantee the
// image indicated actually exists, just that it _should_ exist based off
// the current state of the Function object, in particular the value of
// the Image and ImageDiget fields.
func (f Function) Built() bool {
	// If Image (the override) and ImageDigest (the most recent build stamp) are
	// both empty, the Function is considered unbuilt.
	// TODO: upgrade to a "build complete" timestamp.
	return f.Image != "" || f.ImageDigest != ""
}

// ImageWithDigest returns the full reference to the image including SHA256 Digest.
// If Digest is empty, image:tag is returned.
// TODO: Populate this only on a successful deploy, as this results on a dirty
// git tree on every build.
func (f Function) ImageWithDigest() string {
	// Return image, if Digest is empty
	if f.ImageDigest == "" {
		return f.Image
	}

	lastSlashIdx := strings.LastIndexAny(f.Image, "/")
	imageAsBytes := []byte(f.Image)

	part1 := string(imageAsBytes[:lastSlashIdx+1])
	part2 := string(imageAsBytes[lastSlashIdx+1:])

	// Remove tag from the image name and append SHA256 hash instead
	return part1 + strings.Split(part2, ":")[0] + "@" + f.ImageDigest
}

// DerivedImage returns the derived image name (OCI container tag) of the
// Function whose source is at root, with the default registry for when
// the image has to be calculated (derived).
// The following are equivalent due to the use of DefaultRegistry:
// registry:  docker.io/myname
//            myname
// A full image name consists of registry, image name and tag.
// in form [registry]/[image-name]:[tag]
// example docker.io/alice/my.example.func:latest
// Default if not provided is --registry (a required global setting)
// followed by the provided (or derived) image name.
// TODO: this calculated field should probably be generated on instantiation
// to avoid confusion.
func DerivedImage(root, registry string) (image string, err error) {
	f, err := NewFunction(root)
	if err != nil {
		// an inability to load the Function means it is not yet initialized
		// We could try to be smart here and fall through to the Function name
		// deriviation logic, but that's likely to be confusing.  Better to
		// stay simple and say that derivation of Image depends on first having
		// the Function initialized.
		return
	}

	// If the Function has already had image populated
	// and a new registry hasn't been provided, use this pre-calculated value.
	if f.Image != "" && f.Registry == registry {
		image = f.Image
		return
	}

	// registry is currently required until such time as we support
	// pushing to an implicitly-available in-cluster registry by default.
	if registry == "" {
		err = errors.New("registry name is required")
		return
	}

	// If the Function loaded, and there is not yet an Image set, then this is
	// the first build and no explicit image override was specified.  We should
	// therefore derive the image tag from the defined registry and name.
	// form:    [registry]/[user]/[function]:latest
	// example: quay.io/alice/my.function.name:latest
	// Also nested namespaces should be supported:
	// form:    [registry]/[parent]/[user]/[function]:latest
	// example: quay.io/project/alice/my.function.name:latest
	registry = strings.Trim(registry, "/") // too defensive?
	registryTokens := strings.Split(registry, "/")
	if len(registryTokens) == 1 {
		//namespace provided only 'alice'
		image = DefaultRegistry + "/" + registry + "/" + f.Name
	} else if len(registryTokens) == 2 || len(registryTokens) == 3 {
		// registry/namespace provided `quay.io/alice` or registry/parent-namespace/namespace provided `quay.io/project/alice`
		image = registry + "/" + f.Name
	} else if len(registryTokens) > 3 { // the name of the image is also provided `quay.io/alice/my.function.name`
		err = fmt.Errorf("registry should be either 'namespace', 'registry/namespace' or 'registry/parent/namespace', the name of the image will be derived from the function name.")
		return
	}

	// Explicitly append :latest.  We currently expect source control to drive
	// versioning, rather than rely on Docker Hub tags with explicit version
	// numbers, as is seen in many serverless solutions.  This will be updated
	// to branch name when we add source-driven canary/ bluegreen deployments.
	image = image + ":latest"
	return
}

// assertEmptyRoot ensures that the directory is empty enough to be used for
// initializing a new Function.
func assertEmptyRoot(path string) (err error) {
	// If there exists contentious files (congig files for instance), this Function may have already been initialized.
	files, err := contentiousFilesIn(path)
	if err != nil {
		return
	} else if len(files) > 0 {
		return fmt.Errorf("the chosen directory '%v' contains contentious files: %v.  Has the Service Function already been created?  Try either using a different directory, deleting the Function if it exists, or manually removing the files", path, files)
	}

	// Ensure there are no non-hidden files, and again none of the aforementioned contentious files.
	empty, err := isEffectivelyEmpty(path)
	if err != nil {
		return
	} else if !empty {
		err = errors.New("the directory must be empty of visible files and recognized config files before it can be initialized")
		return
	}
	return
}

// contentiousFiles are files which, if extant, preclude the creation of a
// Function rooted in the given directory.
var contentiousFiles = []string{
	FunctionFile,
}

// contentiousFilesIn the given directoy
func contentiousFilesIn(dir string) (contentious []string, err error) {
	files, err := ioutil.ReadDir(dir)
	for _, file := range files {
		for _, name := range contentiousFiles {
			if file.Name() == name {
				contentious = append(contentious, name)
			}
		}
	}
	return
}

// effectivelyEmpty directories are those which have no visible files
func isEffectivelyEmpty(dir string) (bool, error) {
	// Check for any non-hidden files
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if !strings.HasPrefix(file.Name(), ".") {
			return false, nil
		}
	}
	return true, nil
}

// unmarshalFunction from disk (FunctionFile) using the passed Function as
// its defaults.  If no serialized function exists at path, the Function
// returned is equivalent to the default passed.
func unmarshalFunction(f Function) (Function, error) {
	var err error
	var filename = filepath.Join(f.Root, FunctionFile)

	// Return if there is no file to load, or if there is an error reading.
	if _, err = os.Stat(filename); err != nil {
		if os.IsNotExist(err) {
			err = nil // missing file is not an error.
		}
		return f, err
	}

	// Load the file
	bb, err := ioutil.ReadFile(filename)
	if err != nil {
		return f, err
	}

	// Unmarshal as yaml
	if err = yaml.UnmarshalStrict(bb, &f); err != nil {
		// Return immediately if there are syntactic errors.
		return f, formatUnmarshalError(err)
	}

	return f, validateFunction(f)
}

// Validate Function is logically correct, returning a bundled, and quite
// verbose, formatted error detailing any issues.
func validateFunction(f Function) error {
	var ctr int
	errs := [][]string{
		validateVolumes(f.Volumes),
		ValidateBuildEnvs(f.BuildEnvs),
		ValidateEnvs(f.Envs),
		validateOptions(f.Options),
		ValidateLabels(f.Labels),
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("'%v' contains errors:", FunctionFile))

	for _, ee := range errs {
		if len(ee) > 0 {
			b.WriteString("\n") // Precede each group of errors with a linebreak
		}
		for _, e := range ee {
			ctr++
			b.WriteString("\t" + e)
		}
	}

	if ctr == 0 {
		return nil // Return nil if there were no validation errors.
	}

	return errors.New(b.String())
}

// Format yaml unmarshall error to be more human friendly.
func formatUnmarshalError(err error) error {
	var (
		e      = err.Error()
		rxp    = regexp.MustCompile("not found in type .*")
		header = fmt.Sprintf("'%v' is not valid:\n", FunctionFile)
	)

	if strings.HasPrefix(e, "yaml: unmarshal errors:") {
		e = rxp.ReplaceAllString(e, "is not valid")
		e = strings.Replace(e, "yaml: unmarshal errors:\n", header, 1)
	} else if strings.HasPrefix(e, "yaml:") {
		e = rxp.ReplaceAllString(e, "is not valid")
		e = strings.Replace(e, "yaml: ", header+"  ", 1)
	}

	return errors.New(e)
}

// Regex used during instantiation and validation of various Function fields
// by labels, envs, options, etc.
var (
	regWholeSecret      = regexp.MustCompile(`^{{\s*secret:((?:\w|['-]\w)+)\s*}}$`)
	regKeyFromSecret    = regexp.MustCompile(`^{{\s*secret:((?:\w|['-]\w)+):([-._a-zA-Z0-9]+)\s*}}$`)
	regWholeConfigMap   = regexp.MustCompile(`^{{\s*configMap:((?:\w|['-]\w)+)\s*}}$`)
	regKeyFromConfigMap = regexp.MustCompile(`^{{\s*configMap:((?:\w|['-]\w)+):([-._a-zA-Z0-9]+)\s*}}$`)
	regLocalEnv         = regexp.MustCompile(`^{{\s*env:(\w+)\s*}}$`)
)
