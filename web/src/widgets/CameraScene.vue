<script setup lang="ts">
/**
 * A placeholder camera frame. M0 shows no footage: a quiet line drawing in
 * neutral tokens (so every theme, Night included, tints it), with the last
 * detection's box when there is one.
 */
const { variant, person = false } = defineProps<{ variant: number; person?: boolean }>();
</script>

<template>
  <svg class="scene" viewBox="0 0 320 180" preserveAspectRatio="xMidYMid slice" aria-hidden="true">
    <rect class="sky" width="320" height="180" />
    <template v-if="variant % 3 === 0">
      <!-- a driveway: house, garage door, the drive in perspective -->
      <rect class="far" x="0" y="18" width="196" height="78" />
      <rect class="mid" x="18" y="38" width="96" height="58" />
      <path class="line" d="M18 50h96M18 62h96M18 74h96M18 86h96" />
      <rect class="near" x="138" y="44" width="30" height="52" />
      <polygon class="mid" points="0,180 320,180 262,96 54,96" />
      <path class="near" d="M196 128c10-14 54-16 70 0l6 14h-84Z" />
    </template>
    <template v-else-if="variant % 3 === 1">
      <!-- a deck: boards converging, a rail -->
      <rect class="far" x="0" y="0" width="320" height="78" />
      <polygon class="mid" points="0,180 320,180 320,82 0,82" />
      <path class="line" d="M-90 180 112 82M-40 180 128 82M10 180 144 82M60 180 160 82M110 180 176 82M160 180 192 82M210 180 208 82M260 180 224 82M310 180 240 82M360 180 256 82" />
      <rect class="near" x="0" y="74" width="320" height="6" />
      <path class="near" d="M24 40h5v36h-5ZM92 40h5v36h-5ZM160 40h5v36h-5ZM228 40h5v36h-5ZM296 40h5v36h-5Z" />
      <rect class="near" x="0" y="38" width="320" height="4" />
    </template>
    <template v-else>
      <!-- a backyard: fence, a tree, grass -->
      <rect class="mid" x="0" y="112" width="320" height="68" />
      <path class="far" d="M0 76h320v40H0Z" />
      <path class="line" d="M16 76v40M40 76v40M64 76v40M88 76v40M112 76v40M136 76v40M160 76v40M184 76v40M208 76v40M232 76v40M256 76v40M280 76v40M304 76v40" />
      <circle class="near" cx="240" cy="54" r="38" />
      <circle class="near" cx="268" cy="68" r="26" />
      <rect class="near" x="244" y="80" width="8" height="40" />
    </template>
    <g v-if="person" transform="translate(150 112)">
      <circle class="fig" cx="0" cy="-27" r="5.2" />
      <path class="fig" d="M-7-20h14l2.5 17-3.5 1.2L4-9l-.6 27h-4.2L0 0l-.6 18h-4.2l-.6-27-1.5 9.2L-10.4-3Z" />
      <rect class="box" x="-14" y="-38" width="28" height="60" rx="1" />
    </g>
  </svg>
</template>
