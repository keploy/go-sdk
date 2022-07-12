# Contributing to Keploy

Thank you for your interest in Keploy and for taking the time to contribute to this project. 🙌
Keploy is a project by developers for developers and there are a lot of ways you can contribute.
If you don't know where to start contributing, ask us on our [Slack channel](https://join.slack.com/t/keploy/shared_invite/zt-12rfbvc01-o54cOG0X1G6eVJTuI_orSA).

## Code of conduct

Read our [Code of Conduct](CODE_OF_CONDUCT.md) before contributing

## How can I contribute?

There are many ways in which you can contribute to Keploy.

#### 🐛 Report a bug
Report all issues through GitHub Issues using the [Report a Bug](https://github.com/keploy/keploy/issues/new?assignees=&labels=&template=bug_report.md&title=) template.
To help resolve your issue as quickly as possible, read the template and provide all the requested information.

#### 🛠 File a feature request
We welcome all feature requests, whether it's to add new functionality to an existing extension or to offer an idea for a brand new extension.
File your feature request through GitHub Issues using the [Feature Request](https://github.com/keploy/keploy/issues/new?assignees=&labels=&template=feature_request.md&title=) template.

#### 📝 Improve the documentation
In the process of shipping features quickly, we may forget to keep our docs up to date. You can help by suggesting improvements to our documentation using the [Documentation Improvement](https://github.com/keploy/docs/issues) template!

#### ⚙️ Close a Bug / Feature issue
We welcome contributions that help make keploy bug free & improve the experience of our users. You can also find issues tagged [Good First Issues](https://github.com/keploy/keploy/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

# How to Contribute

## Prerequisites

Make sure that the following prequisites are installed in your Operating System before you start contributing to the project - : 

- [Go](https://go.dev/)

To verify run :

```
go version
```

## Set up your Local Development Environment

Follow the following instructions to start contributing - 

1 . Fork [this](https://github.com/keploy/go-sdk.git)

2 . Clone the copy of your forked project

```
git clone https://github.com/<your-github-username>/go-sdk.git
```

3 . Navigate to the project directory

```
cd go-sdk
```
4 . Add a remote reference (upstream) to the original repository.

```
git remote add upstream https://github.com/keploy/go-sdk.git
```

5 . Always take a pull from the upstream repository to your master branch to keep it updated with the main project.

```
git pull upstream main
```

6 . Configure the pre-commit hook by running the following path.

```
git config core.hooksPath .githooks && chmod +x .githooks/*
```

7 . create a new branch

```
git checkout -b <your-branch-name>
```

8 . Install the dependencies by running the following command

```
go get -u github.com/keploy/go-sdk
```

9 . Make the desired changes

10 . Track your changes 

```
git status
```

11 . Add your changes to staging area

```
git add .
```

12 . Commit your changes. [Please refer to this article to know more about the commit message convention followed by Keploy.](https://www.conventionalcommits.org/en/v1.0.0/)

```
git commit -m "<commit message>"
```

13 . While you are working on your branch, other developers may update the `main` branch with their branch. This action means your branch is now out of date with the `main` branch and missing content which may lead to merge conflicts. So to avoid this fetch the new changes, follow along:

```
git checkout main
git fetch origin main
git merge upstream/main
git push origin
```

14 . Now you need to merge the `main` branch into your branch. This can be done in the following way -:

```
git checkout <your_branch_name>
git merge main
```

15 . Push the committed changes in your feature branch to your remote repository.

```
git push -u origin <your_branch_name>
```

Once you’ve committed and pushed all of your changes to GitHub, go to the page for your fork on GitHub, select your development branch, and click the compare & pull request button. This will create a Pull Request for your branch. Wait untill a contributor give you a feedback on the contribution. After the feedback your branch will be merged into main branch of the repository.