<script lang="ts">
  import logo from '$lib/assets/logo.svg';

  import '../app.css';

  let { children } = $props();
</script>

<svelte:head>
  <link rel="icon" href={logo} />
</svelte:head>

{@render children()}
