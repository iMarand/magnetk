# Marks the wheel as platform-specific: it bundles native libraries
# (libmagnetk + msquic) in magnetk/lib. Metadata lives in pyproject.toml.
from setuptools import setup
from setuptools.dist import Distribution


class BinaryDistribution(Distribution):
    def has_ext_modules(self):
        return True


try:
    from wheel.bdist_wheel import bdist_wheel as _bdist_wheel

    class bdist_wheel(_bdist_wheel):
        def finalize_options(self):
            super().finalize_options()
            self.root_is_pure = False

        def get_tag(self):
            _, _, plat = super().get_tag()
            return "py3", "none", plat  # pure-Python code, native libs via ctypes

    cmdclass = {"bdist_wheel": bdist_wheel}
except ImportError:  # wheel not installed: sdist only
    cmdclass = {}

setup(distclass=BinaryDistribution, cmdclass=cmdclass)
